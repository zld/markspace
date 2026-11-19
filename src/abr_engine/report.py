"""Per-segment and summary reporting: JSON Lines and terminal table."""
from __future__ import annotations

import json

from .model import DecisionRecord
from .simulator import SimulationResult


def record_dict(record: DecisionRecord) -> dict:
    return {
        "type": "segment",
        "segment": record.segment,
        "bitrate_bps": record.bitrate_bps,
        "estimated_bps": round(record.estimated_bps, 3),
        "download_s": round(record.download_s, 6),
        "buffer_s": round(record.buffer_s, 6),
        "rebuffer_s": round(record.rebuffer_s, 6),
    }


def summary_dict(result: SimulationResult) -> dict:
    return {
        "type": "summary",
        "segments": len(result.records),
        "switch_count": result.switch_count,
        "total_rebuffer_s": round(result.total_rebuffer_s, 6),
        "final_buffer_s": round(result.final_buffer_s, 6),
    }


def format_jsonl(result: SimulationResult) -> str:
    """One JSON object per segment, then one summary object on the last line."""
    lines = [json.dumps(record_dict(r), sort_keys=True) for r in result.records]
    lines.append(json.dumps(summary_dict(result), sort_keys=True))
    return "\n".join(lines) + "\n"


def format_table(result: SimulationResult) -> str:
    header = (
        f"{'seg':>6} {'bitrate_bps':>12} {'est_bps':>12}"
        f" {'dl_s':>8} {'buf_s':>8} {'rebuf_s':>8}"
    )
    lines = [header, "-" * len(header)]
    for r in result.records:
        lines.append(
            f"{r.segment:>6} {r.bitrate_bps:>12} {r.estimated_bps:>12.0f}"
            f" {r.download_s:>8.3f} {r.buffer_s:>8.3f} {r.rebuffer_s:>8.3f}"
        )
    summary = summary_dict(result)
    lines.append("-" * len(header))
    lines.append(
        f"segments={summary['segments']}"
        f" switch_count={summary['switch_count']}"
        f" total_rebuffer_s={summary['total_rebuffer_s']:.3f}"
        f" final_buffer_s={summary['final_buffer_s']:.3f}"
    )
    return "\n".join(lines) + "\n"
