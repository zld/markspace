"""Batch scheduling: walk a directory of PCM files and process each one.

``process_file`` is the single code path for detection -- the CLI single-file
command and the batch runner both call it, so results are identical by
construction. Files are processed in sorted order; a failing file is logged
with its error and the remaining files still run.
"""
from __future__ import annotations

import hashlib
from dataclasses import asdict, dataclass
from pathlib import Path

import numpy as np

from . import audio
from .detector import Detector, DetectorParams
from .segments import Segment, SegmentParams, build_segments
from .store import Store


class InputError(Exception):
    """Input-level problem (missing directory, no PCM files, escaping path)."""


@dataclass(frozen=True)
class BatchConfig:
    detector: DetectorParams
    segments: SegmentParams
    chunk_seconds: float = 5.0  # streaming chunk size; bounds memory use

    def params_dict(self) -> dict:
        return {"detector": asdict(self.detector), "segments": asdict(self.segments)}


def file_digest(path: Path) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as fh:
        for block in iter(lambda: fh.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()


def _segment_stats(path: Path, seg: Segment, chunk: int = 1 << 18) -> dict:
    """Peak and average RMS over exact sample bounds, read in bounded slices."""
    peak = 0.0
    sum_sq = 0.0
    count = 0
    pos = seg.start_sample
    while pos < seg.end_sample:
        data = audio.read_range(path, pos, min(seg.end_sample, pos + chunk))
        if data.size == 0:
            break
        peak = max(peak, float(np.max(np.abs(data))))
        sum_sq += float(np.sum(data * data))
        count += data.size
        pos += data.size
    avg_rms = float(np.sqrt(sum_sq / count)) if count else 0.0
    return {
        "start_sample": seg.start_sample,
        "end_sample": seg.end_sample,
        "duration_seconds": seg.duration_seconds,
        "peak": peak,
        "avg_rms": avg_rms,
    }


def process_file(path, config: BatchConfig) -> dict:
    """Detect segments in one PCM file. Raises AudioError on bad input."""
    path = Path(path)
    info = audio.read_info(path)
    detector = Detector(config.detector, info.sample_rate)
    chunk = max(detector.frame, int(info.sample_rate * config.chunk_seconds))
    for block in audio.iter_mono(path, chunk):
        detector.feed(block)
    result = detector.finish(info.num_samples)
    read_window = lambda s, e: audio.read_range(path, s, e)  # noqa: E731
    segs = build_segments(result, config.segments, read_window)
    seg_dicts = [_segment_stats(path, s) for s in segs]
    active = sum(s["end_sample"] - s["start_sample"] for s in seg_dicts)
    silence_samples = info.num_samples - active
    return {
        "path": str(path),
        "digest": file_digest(path),
        "sample_rate": info.sample_rate,
        "channels": info.channels,
        "num_samples": info.num_samples,
        "noise_floor": result.noise_floor,
        "high_threshold": result.high_threshold,
        "low_threshold": result.low_threshold,
        "segments": seg_dicts,
        # Leading and trailing silence are included in this total.
        "silence_total_seconds": silence_samples / info.sample_rate,
        "silence_includes_edges": True,
    }


def _ensure_within(root: Path, path: Path) -> None:
    if not path.resolve().is_relative_to(root):
        raise InputError(f"{path.name}: resolves outside the input root; skipped")


def run_batch(input_dir, db_path, config: BatchConfig) -> dict:
    root = Path(input_dir).resolve()
    if not root.is_dir():
        raise InputError(f"input directory not found: {input_dir}")
    files = sorted(p for p in root.glob("*.wav") if p.is_file())
    if not files:
        raise InputError(f"no .wav PCM files found in {input_dir}")

    results: list[dict] = []
    errors: list[dict] = []
    with Store(db_path) as store:
        job_id = store.get_or_create_job(config.params_dict())
        for path in files:
            _ensure_within(root, path)
            try:
                res = process_file(path, config)
                store.save_file_result(
                    job_id, res["path"], res["digest"],
                    {"sample_rate": res["sample_rate"], "channels": res["channels"],
                     "num_samples": res["num_samples"]},
                    res["segments"],
                )
                results.append(res)
            except Exception as exc:  # file-level failure: record and continue
                try:
                    digest = file_digest(path)
                except OSError:
                    digest = None
                store.save_file_error(job_id, str(path), digest, str(exc))
                errors.append({"path": str(path), "error": str(exc)})
    return {
        "job_id": job_id,
        "files": len(files),
        "succeeded": len(results),
        "failed": len(errors),
        "segments": sum(len(r["segments"]) for r in results),
        "silence_total_seconds": sum(r["silence_total_seconds"] for r in results),
        "results": results,
        "errors": errors,
    }
