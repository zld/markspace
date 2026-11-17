"""Terminal summary and per-frame JSONL output.

Both the stdout summary and the optional ``--frames-out`` JSONL manifest are
derived from the same :class:`~frameaudit.repair.RepairResult` /
:class:`~frameaudit.stats.FrameStats` objects, so they can never disagree.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from .errors import OutputError
from .repair import RepairResult
from .stats import FrameStats


def build_summary(
    source: str,
    stream_index: int,
    codec_name: str,
    frame_rate: Any,
    stats: FrameStats,
    repair: RepairResult,
    reorder_limit: int,
    fault_manifest: str | None,
) -> dict[str, Any]:
    """Assemble the JSON-serialisable per-file summary."""
    return {
        "source": source,
        "stream_index": stream_index,
        "codec": codec_name,
        "frame_rate": (
            f"{frame_rate.numerator}/{frame_rate.denominator}"
            if frame_rate is not None
            else None
        ),
        "reorder_limit": reorder_limit,
        "fault_manifest": fault_manifest,
        "time_base": (
            f"{repair.time_base.numerator}/{repair.time_base.denominator}"
        ),
        "decoded_frames": stats.decoded_frames,
        "i_frames": stats.i_frames,
        "p_frames": stats.p_frames,
        "b_frames": stats.b_frames,
        "unknown_frames": stats.unknown_frames,
        "timestamp_repaired": repair.repaired_count,
        "repair_reasons": dict(sorted(repair.reason_counts.items())),
        "max_jump_ms": repair.max_jump_ms,
        "luma": {
            "mean": stats.luma_mean,
            "min": stats.luma_min,
            "max": stats.luma_max,
            "std": stats.luma_std,
        },
    }


def frame_record_to_jsonl(record: Any) -> dict[str, Any]:
    """Convert one repaired frame record to a JSONL row."""
    frame = record.frame
    return {
        "order_index": record.order_index,
        "decode_index": frame.decode_index,
        "pict_type": frame.pict_type,
        "keyframe": frame.keyframe,
        "original_pts": record.original_pts,
        "repaired_pts": record.repaired_pts,
        "time_base": f"{record.time_base.numerator}/{record.time_base.denominator}",
        "original_pts_ms": record.original_pts_ms,
        "repaired_pts_ms": record.repaired_pts_ms,
        "repair_reason": record.repair_reason,
    }


def ensure_output_path(path: str | Path, overwrite: bool = False) -> Path:
    """Validate an output path, refusing to clobber existing files."""
    path = Path(path)
    if path.exists() and not overwrite:
        raise OutputError(f"refusing to overwrite existing output: {path}")
    parent = path.parent
    if not parent.exists():
        raise OutputError(f"output directory does not exist: {parent}")
    return path


def write_frames_jsonl(
    path: str | Path,
    repair: RepairResult,
    overwrite: bool = False,
) -> Path:
    """Write one JSON object per line, one line per presentation-order frame."""
    path = ensure_output_path(path, overwrite)
    try:
        with path.open("w", encoding="utf-8") as handle:
            for record in repair.records:
                handle.write(
                    json.dumps(frame_record_to_jsonl(record), sort_keys=True)
                    + "\n"
                )
    except OSError as exc:
        raise OutputError(f"failed to write {path}: {exc}") from exc
    return path


def format_text_summary(summary: dict[str, Any]) -> str:
    """Render a human-friendly terminal block for ``--format text``."""
    lines = [
        f"frameaudit report: {summary['source']}",
        f"  stream {summary['stream_index']} ({summary['codec']}), "
        f"{summary['frame_rate']} fps, time_base {summary['time_base']}",
        f"  decoded frames : {summary['decoded_frames']}",
        f"  frame types    : I={summary['i_frames']} P={summary['p_frames']} "
        f"B={summary['b_frames']} unknown={summary['unknown_frames']}",
        f"  PTS repaired   : {summary['timestamp_repaired']} "
        f"{summary['repair_reasons']}",
        f"  max jump (ms)  : {summary['max_jump_ms']}",
        f"  luma           : mean={summary['luma']['mean']} "
        f"min={summary['luma']['min']} max={summary['luma']['max']} "
        f"std={summary['luma']['std']}",
    ]
    return "\n".join(lines)
