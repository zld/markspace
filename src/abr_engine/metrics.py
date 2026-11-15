"""Aggregation of per-segment decision records into run summaries."""
from __future__ import annotations

import math

from abr_engine.models import DecisionRecord


def summarize(records: list[DecisionRecord]) -> dict:
    """Compute switch and rebuffer totals over one simulation run."""
    ups = sum(1 for r in records if r.switch == "up")
    downs = sum(1 for r in records if r.switch == "down")
    total_rebuffer = sum(r.rebuffer_seconds for r in records)
    return {
        "segment_count": len(records),
        "switch_count": ups + downs,
        "up_switches": ups,
        "down_switches": downs,
        "total_rebuffer_seconds": round(total_rebuffer, 6),
        "max_segment_rebuffer_seconds": round(
            max((r.rebuffer_seconds for r in records), default=0.0), 6),
        "fallback_count": sum(1 for r in records if r.fallback),
    }


def ensure_finite(value, path: str = "result") -> None:
    """Raise ValueError if any float in a JSON-bound structure is non-finite."""
    if isinstance(value, float):
        if not math.isfinite(value):
            raise ValueError(f"{path} contains a non-finite number")
    elif isinstance(value, dict):
        for key, item in value.items():
            ensure_finite(item, f"{path}.{key}")
    elif isinstance(value, (list, tuple)):
        for i, item in enumerate(value):
            ensure_finite(item, f"{path}[{i}]")
