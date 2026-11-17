"""Segment WAV export with atomic replacement and path containment.

Each slice is written to a temporary file in the output directory and renamed
into place only after its header is finalized, so a crash never leaves a
half-written segment behind.  Exports are confined to one directory and can
never overwrite a source file; failure of one input does not block others
(isolation is handled by :mod:`audiocut.batch`).
"""

from __future__ import annotations

import json
import os
import tempfile
from dataclasses import dataclass
from pathlib import Path

from .detector import DetectionResult
from .wavio import WavReader, WavWriter, copy_segment


class ExportError(Exception):
    """Raised on unsafe output paths or export failures."""


def _resolve_inside(outdir: Path, target: Path) -> Path:
    """Resolve ``target`` and guarantee it stays inside ``outdir``."""

    base = outdir.resolve()
    resolved = target.resolve()
    if base != resolved and base not in resolved.parents:
        raise ExportError(f"export path escapes output directory: {target}")
    return resolved


def _is_without(base: Path, target: Path) -> bool:
    """True if resolved ``target`` is outside the resolved ``base`` directory."""

    base_resolved = base.resolve()
    target_resolved = target.resolve()
    return base_resolved != target_resolved and base_resolved not in target_resolved.parents


def _segment_filename(stem: str, index: int, start: int, end: int) -> str:
    return f"{stem}_seg{index:04d}_{start}_{end}.wav"


@dataclass
class ExportedSegment:
    index: int
    path: str
    start_sample: int
    end_sample: int


def export_segments(
    source: str | Path,
    result: DetectionResult,
    outdir: str | Path,
) -> list[ExportedSegment]:
    """Write one WAV per detected segment plus nothing else (manifest elsewhere)."""

    outdir_path = Path(outdir)
    outdir_path.mkdir(parents=True, exist_ok=True)
    source_path = Path(source).resolve()
    # A source inside the output directory could collide with an export name.
    if not _is_without(outdir_path, source_path):
        raise ExportError(f"refusing to overwrite source inside output dir: {source_path}")

    exported: list[ExportedSegment] = []
    stem = Path(source).stem
    for segment in result.segments:
        name = _segment_filename(stem, segment.index, segment.start_sample, segment.end_sample)
        final_path = _resolve_inside(outdir_path, outdir_path / name)
        if final_path == source_path:
            raise ExportError(f"refusing to overwrite source file: {source_path}")

        fd, tmp_name = tempfile.mkstemp(prefix=".audiocut-", suffix=".wav.tmp", dir=outdir_path)
        os.close(fd)
        try:
            with WavReader(source) as reader, WavWriter(
                tmp_name, reader.sample_rate, reader.channels, reader.sample_width
            ) as writer:
                copy_segment(reader, writer, segment.start_sample, segment.end_sample)
            os.replace(tmp_name, final_path)  # atomic on the same filesystem
        except Exception:
            try:
                os.unlink(tmp_name)
            except OSError:
                pass
            raise
        exported.append(
            ExportedSegment(segment.index, str(final_path), segment.start_sample, segment.end_sample)
        )
    return exported


def write_manifest(
    manifest_path: str | Path,
    outdir: str | Path,
    result: DetectionResult,
    exported: list[ExportedSegment],
    source: str | Path,
) -> str:
    """Atomically write a JSON manifest describing one source's slices."""

    outdir_path = Path(outdir)
    target = _resolve_inside(outdir_path, Path(manifest_path))
    source_path = Path(source).resolve()
    if target == source_path:
        raise ExportError(f"refusing to overwrite source file: {source_path}")

    payload = {
        "source": str(source_path),
        "sample_rate": result.sample_rate,
        "n_frames_total": result.n_frames_total,
        "config_fingerprint": result.config_fingerprint,
        "noise_floor": result.noise_floor,
        "low_threshold": result.low_threshold,
        "high_threshold": result.high_threshold,
        "silence_total_seconds": result.silence_total_seconds,
        "segments": [
            {
                "index": item.index,
                "path": item.path,
                "start_sample": item.start_sample,
                "end_sample": item.end_sample,
            }
            for item in exported
        ],
        "silences": [
            {"start_sample": r.start_sample, "end_sample": r.end_sample}
            for r in result.silences
        ],
    }
    fd, tmp_name = tempfile.mkstemp(prefix=".audiocut-manifest-", suffix=".tmp", dir=outdir_path)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(payload, handle, indent=2, sort_keys=True)
        os.replace(tmp_name, target)
    except Exception:
        try:
            os.unlink(tmp_name)
        except OSError:
            pass
        raise
    return str(target)
