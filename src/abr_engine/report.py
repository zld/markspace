"""Aggregation and output formatting for simulation results."""
from __future__ import annotations

import json

from .models import ValidationError
from .simulator import SimulationResult

MAX_JSON_BYTES = 32 * 1024 * 1024

SUMMARY_KEYS = (
    "segment_count",
    "switch_up_count",
    "switch_down_count",
    "switch_count",
    "stall_event_count",
    "stall_duration_seconds",
    "final_buffer_seconds",
)


def summarize(result: SimulationResult) -> dict:
    """Aggregate per-segment records into stable summary fields."""
    records = result.records
    up = sum(1 for r in records if r.switch == "up")
    down = sum(1 for r in records if r.switch == "down")
    stall_events = sum(1 for r in records if r.stall_s > 0.0)
    stall_total = sum(r.stall_s for r in records)
    return {
        "segment_count": len(records),
        "switch_up_count": up,
        "switch_down_count": down,
        "switch_count": up + down,
        "stall_event_count": stall_events,
        "stall_duration_seconds": round(stall_total, 6),
        "final_buffer_seconds": round(result.final_buffer_s, 6),
    }


def to_payload(result: SimulationResult) -> dict:
    """Build the full JSON-serializable result payload."""
    return {
        "version": 1,
        "units": {"bitrate": "bit/s", "duration": "s", "size": "byte"},
        "summary": summarize(result),
        "continuation_intervals_seconds": [
            [round(start, 6), round(end, 6)] for start, end in result.continuation_intervals
        ],
        "segments": [
            {
                "index": r.index,
                "time_seconds": round(r.time_s, 6),
                "estimated_bandwidth_bps": round(r.estimated_bandwidth_bps, 3),
                "confidence": r.confidence,
                "buffer_before_seconds": round(r.buffer_before_s, 6),
                "buffer_after_seconds": round(r.buffer_after_s, 6),
                "selected_bitrate_bps": r.selected_bitrate_bps,
                "download_seconds": round(r.download_s, 6),
                "switch": r.switch,
                "stall_seconds": round(r.stall_s, 6),
            }
            for r in result.records
        ],
    }


def dumps_payload(payload: dict) -> str:
    """Serialize a payload, rejecting non-finite values and oversize output."""
    try:
        text = json.dumps(payload, indent=2, allow_nan=False)
    except (ValueError, TypeError) as exc:
        raise ValidationError(f"result payload is not JSON-safe: {exc}") from exc
    size = len(text.encode("utf-8"))
    if size > MAX_JSON_BYTES:
        raise ValidationError(f"result JSON of {size} bytes exceeds limit {MAX_JSON_BYTES}")
    return text


def format_summary(summary: dict) -> str:
    """Render the summary as a stable terminal report."""
    lines = ["ABR simulation summary"]
    for key in SUMMARY_KEYS:
        if key not in summary:
            raise ValidationError(f"summary is missing required field {key!r}")
        lines.append(f"  {key}: {summary[key]}")
    return "\n".join(lines)
