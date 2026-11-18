"""Export detected segments as WAV files and JSON manifests.

Every file is written to a temporary sibling and atomically replaced, so a
failed export never truncates previously written results.
"""

from __future__ import annotations

import io
import json
import os
import tempfile
import wave
from dataclasses import dataclass
from pathlib import Path
from typing import Sequence

import numpy as np

from .config import DetectionConfig


class ExportError(Exception):
    """Raised when an export target cannot be written."""


@dataclass(frozen=True)
class ExportSegment:
    index: int
    start: int
    end: int
    fade_in: int
    fade_out: int
    peak_dbfs: float
    rms_dbfs: float

    @property
    def length(self) -> int:
        return self.end - self.start


@dataclass(frozen=True)
class ExportResult:
    directory: Path
    manifest: Path
    wav_files: tuple[Path, ...]


def _atomic_write_bytes(target: Path, payload: bytes) -> None:
    target.parent.mkdir(parents=True, exist_ok=True)
    fd, temp_name = tempfile.mkstemp(
        prefix=f".{target.name}.", suffix=".tmp", dir=str(target.parent)
    )
    temp = Path(temp_name)
    try:
        with os.fdopen(fd, "wb") as handle:
            handle.write(payload)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temp, target)
    except OSError as exc:
        try:
            temp.unlink(missing_ok=True)
        except OSError:
            pass
        raise ExportError(f"cannot write {target}: {exc.strerror}") from exc


def _apply_fades(samples: np.ndarray, fade_in: int, fade_out: int) -> np.ndarray:
    out = np.array(samples, dtype=np.float64, copy=True)
    if fade_in > 0 and out.size:
        ramp = np.linspace(0.0, 1.0, fade_in, endpoint=True)
        out[:fade_in] *= ramp
    if fade_out > 0 and out.size:
        ramp = np.linspace(1.0, 0.0, fade_out, endpoint=True)
        out[-fade_out:] *= ramp
    return out


def _wav_bytes(samples: np.ndarray, sample_rate: int) -> bytes:
    clipped = np.clip(samples, -1.0, 1.0) * 32767.0
    pcm = np.round(clipped).astype("<i2")
    buffer = io.BytesIO()
    with wave.open(buffer, "wb") as handle:
        handle.setnchannels(1)
        handle.setsampwidth(2)
        handle.setframerate(sample_rate)
        handle.writeframes(pcm.tobytes())
    return buffer.getvalue()


def _safe_stem(relative_path: str) -> str:
    stem = Path(relative_path).stem
    cleaned = "".join(ch if ch.isalnum() or ch in "-_." else "_" for ch in stem)
    return cleaned or "input"


def export_run(
    *,
    dest_root: Path,
    relative_path: str,
    samples: np.ndarray,
    sample_rate: int,
    total_samples: int,
    segments: Sequence[ExportSegment],
    silences: Sequence[tuple[int, int]],
    config: DetectionConfig,
    run_id: int,
    fingerprint: str,
) -> ExportResult:
    stem = _safe_stem(relative_path)
    parent = Path(relative_path).parent
    directory = dest_root / parent / stem if str(parent) != "." else dest_root / stem
    try:
        directory.mkdir(parents=True, exist_ok=True)
    except OSError as exc:
        raise ExportError(f"cannot create {directory}: {exc.strerror}") from exc

    wav_files: list[Path] = []
    manifest_segments: list[dict[str, object]] = []
    for segment in segments:
        audio = _apply_fades(
            samples[segment.start : segment.end], segment.fade_in, segment.fade_out
        )
        name = f"{stem}_{segment.index:04d}.wav"
        target = directory / name
        _atomic_write_bytes(target, _wav_bytes(audio, sample_rate))
        wav_files.append(target)
        manifest_segments.append(
            {
                "index": segment.index,
                "file": name,
                "start_sample": segment.start,
                "end_sample": segment.end,
                "length_samples": segment.length,
                "fade_in_samples": segment.fade_in,
                "fade_out_samples": segment.fade_out,
                "start_seconds": round(segment.start / sample_rate, 6),
                "end_seconds": round(segment.end / sample_rate, 6),
                "peak_dbfs": round(segment.peak_dbfs, 2),
                "rms_dbfs": round(segment.rms_dbfs, 2),
            }
        )

    silence_samples = sum(end - start for start, end in silences)
    manifest = {
        "schema_version": 1,
        "input": relative_path,
        "run_id": run_id,
        "fingerprint": fingerprint,
        "sample_rate": sample_rate,
        "total_samples": total_samples,
        "duration_seconds": round(total_samples / sample_rate, 6),
        "segment_count": len(segments),
        "segments": manifest_segments,
        "silence_samples": silence_samples,
        "silence_seconds": round(silence_samples / sample_rate, 6),
        "silences": [
            {"start_sample": start, "end_sample": end, "length_samples": end - start}
            for start, end in silences
        ],
        "config": config.to_dict(),
        "config_digest": config.digest(),
    }
    manifest_path = directory / "manifest.json"
    payload = json.dumps(manifest, sort_keys=True, indent=2).encode("utf-8")
    _atomic_write_bytes(manifest_path, payload)
    return ExportResult(directory=directory, manifest=manifest_path, wav_files=tuple(wav_files))


def export_index(dest_root: Path, entries: Sequence[dict[str, object]]) -> Path:
    target = dest_root / "index.json"
    payload = json.dumps(
        {"schema_version": 1, "files": list(entries)}, sort_keys=True, indent=2
    ).encode("utf-8")
    _atomic_write_bytes(target, payload)
    return target
