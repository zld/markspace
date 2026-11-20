"""Batch report serialization and output re-verification."""

from __future__ import annotations

import hashlib
import json
import math
from pathlib import Path

import numpy as np
import soundfile as sf

from .loudness import LoudnessMeter
from .resample import Resampler, expected_output_frames

#: Absolute tolerances used when recomputing output statistics.
LUFS_TOLERANCE = 0.2
RMS_TOLERANCE = 1e-6
FRAME_TOLERANCE = 0

PEAK_CEILING_LINEAR = 10 ** (-1.0 / 20.0)  # -1 dBFS


def sha256_file(path: Path) -> str:
    """Stream a file through SHA-256 without loading it wholesale."""
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for block in iter(lambda: handle.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()


def _finite_or_none(value: float | None) -> float | None:
    if value is None:
        return None
    value = float(value)
    return value if math.isfinite(value) else None


def build_record(source: Path, relative: Path, output: Path,
                 source_sha: str, params: dict, input_frames: int,
                 output_frames: int, zero_pad_frames: int,
                 before: dict, after: dict, gain: dict,
                 channels: int) -> dict:
    """Assemble one per-file report entry with all required fields."""
    return {
        "source": str(relative),
        "source_sha256": source_sha,
        "output": str(output),
        "status": "ok",
        "output_sample_rate": params["target_rate"],
        "output_channels": channels,
        "algorithm": {
            "resampler": "polyphase_windowed_sinc",
            "taps": params["taps"],
            "loudness": "ITU-R BS.1770-4",
            "block_ms": 400,
            "hop_ms": 100,
            "peak_ceiling_dbfs": -1.0,
        },
        "input_sample_rate": params["source_rate"],
        "input_frames": int(input_frames),
        "output_frames": int(output_frames),
        "expected_output_frames": expected_output_frames(
            input_frames, params["source_rate"], params["target_rate"]),
        "zero_pad_frames": int(zero_pad_frames),
        "target_lufs": _finite_or_none(params.get("target_lufs")),
        "gain": {
            "linear": float(gain["linear"]),
            "db": float(gain["db"]),
            "peak_limited": bool(gain["peak_limited"]),
            "silence_skipped": bool(gain["silence_skipped"]),
        },
        "rms_before": float(before["rms"]),
        "rms_after": float(after["rms"]),
        "lufs_before": _finite_or_none(before["lufs"]),
        "lufs_after": _finite_or_none(after["lufs"]),
        "clipped_samples_before": int(before["clipped"]),
        "clipped_samples_after": int(after["clipped"]),
    }


def write_report(report: dict, path: Path) -> None:
    """Atomically write the JSON report (temp file then replace)."""
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + ".tmp")
    with open(tmp, "w", encoding="utf-8") as handle:
        json.dump(report, handle, indent=2, sort_keys=True,
                  allow_nan=False)
        handle.write("\n")
    tmp.replace(path)


def load_report(path: Path) -> dict:
    with open(path, "r", encoding="utf-8") as handle:
        return json.load(handle)


def _recompute_stats(path: Path, channels: int, sample_rate: int
                     ) -> tuple[dict, int]:
    """Stream an output WAV and recompute its loudness statistics."""
    meter = LoudnessMeter(channels, sample_rate)
    frames = 0
    with sf.SoundFile(str(path)) as handle:
        while True:
            block = handle.read(4096, dtype="float64", always_2d=True)
            if block.shape[0] == 0:
                break
            if not np.all(np.isfinite(block)):
                raise ValueError("non-finite sample in output")
            meter.add(block)
            frames += block.shape[0]
    stats = {
        "rms": meter.rms(),
        "lufs": meter.integrated_lufs(),
        "clipped": meter.clipped(),
    }
    return stats, frames


def verify_report(report_path: Path) -> tuple[bool, list[dict]]:
    """Re-read every output and recompute reported statistics.

    Returns ``(ok, discrepancies)`` where each discrepancy pinpoints the
    file and field that mismatched.
    """
    report = load_report(report_path)
    discrepancies: list[dict] = []
    for entry in report.get("files", []):
        if entry.get("status") != "ok":
            continue
        out = Path(entry["output"])
        if not out.is_file():
            discrepancies.append({"source": entry["source"],
                                  "field": "output",
                                  "problem": "file missing"})
            continue
        if sha256_file(out) != entry.get("output_sha256"):
            discrepancies.append({"source": entry["source"],
                                  "field": "output_sha256",
                                  "problem": "output content changed"})
        try:
            stats, frames = _recompute_stats(
                out, entry["output_channels"],
                entry["output_sample_rate"])
        except Exception as exc:
            discrepancies.append({"source": entry["source"],
                                  "field": "decode",
                                  "problem": str(exc)})
            continue
        if frames != entry["output_frames"]:
            discrepancies.append({
                "source": entry["source"], "field": "output_frames",
                "expected": entry["output_frames"], "actual": frames})
        if abs(stats["rms"] - entry["rms_after"]) > RMS_TOLERANCE:
            discrepancies.append({
                "source": entry["source"], "field": "rms_after",
                "expected": entry["rms_after"], "actual": stats["rms"]})
        if stats["clipped"] != entry["clipped_samples_after"]:
            discrepancies.append({
                "source": entry["source"],
                "field": "clipped_samples_after",
                "expected": entry["clipped_samples_after"],
                "actual": stats["clipped"]})
        exp_lufs = entry["lufs_after"]
        if exp_lufs is None:
            if stats["lufs"] is not None:
                discrepancies.append({
                    "source": entry["source"], "field": "lufs_after",
                    "expected": None, "actual": stats["lufs"]})
        elif stats["lufs"] is None or abs(
                stats["lufs"] - exp_lufs) > LUFS_TOLERANCE:
            discrepancies.append({
                "source": entry["source"], "field": "lufs_after",
                "expected": exp_lufs, "actual": stats["lufs"]})
    return not discrepancies, discrepancies
