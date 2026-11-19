"""Turn audit results into the numbers the reports show.

This module owns the derived summary fields, so the terminal table and the JSON
document read the same values from the same object instead of each computing
them.  Nothing here mutates an ``AuditResult``.
"""

from __future__ import annotations

from typing import Any

from .model import AuditResult

__all__ = ["summary_fields", "result_to_json", "REPORT_FIELDS"]


#: Field order used by the JSON Lines output.  The terminal table prints the
#: same names, which makes the terminal-vs-JSON equality check a straight
#: comparison rather than a translation table.
REPORT_FIELDS: tuple[str, ...] = (
    "file",
    "decoded_frames",
    "i_frames",
    "p_frames",
    "b_frames",
    "repaired_timestamps",
    "max_timestamp_jump_us",
    "truncated",
)


def _frame_document(result: AuditResult) -> list[dict[str, Any]]:
    return [
        {
            "position": frame.position,
            "decode_index": frame.frame.index,
            "type": frame.pict_type,
            "original_pts": frame.original_pts,
            "original_pts_us": frame.original_pts_us,
            "repaired_pts": frame.repaired_pts,
            "repaired_pts_us": frame.repaired_pts_us,
            "repair": frame.kind.value,
            "applied": frame.applied,
        }
        for frame in result.frames
    ]


def summary_fields(result: AuditResult) -> dict[str, Any]:
    """The flat scalar fields shown in the terminal table."""
    return {
        "file": result.display_path,
        "decoded_frames": result.decoded_frames,
        "i_frames": result.i_frames,
        "p_frames": result.p_frames,
        "b_frames": result.b_frames,
        "repaired_timestamps": result.repaired_timestamps,
        "max_timestamp_jump_us": result.max_timestamp_jump_us,
        "truncated": result.truncated,
    }


def result_to_json(result: AuditResult) -> dict[str, Any]:
    """Full per-file document: summary, provenance, anomalies and every frame."""
    stream = result.stream
    return {
        **summary_fields(result),
        "path": result.path,
        "ok": result.ok,
        "error": result.error,
        "error_reason": result.error_reason,
        "skipped_reason": result.skipped_reason,
        "policy": result.policy.value,
        "max_frames": result.max_frames,
        "detected_anomalies": result.detected_anomalies,
        "repair_breakdown": result.repair_breakdown,
        "other_frames": result.other_frames,
        "decode_errors": list(result.decode_errors),
        "warnings": list(result.warnings),
        "stream": (
            {
                "format": stream.format_name,
                "codec": stream.codec_name,
                "width": stream.width,
                "height": stream.height,
                "time_base": str(stream.time_base),
                "average_rate": str(stream.average_rate) if stream.average_rate else None,
                "container_frames": stream.container_frames,
                "container_duration_us": stream.container_duration_us,
                "start_time_us": stream.start_time_us,
            }
            if stream is not None
            else None
        ),
        "frames": _frame_document(result),
    }
