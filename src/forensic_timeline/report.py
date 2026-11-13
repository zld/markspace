"""Terminal summary and machine-readable JSON reporting."""

from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import Any, TextIO

SUMMARY_KEYS = [
    "batch_id",
    "image",
    "manifest",
    "timezone",
    "events",
    "duplicates",
    "rejected_records",
    "time_anomalies",
    "hash_mismatches",
    "missing_blocks",
    "extra_blocks",
    "bad_hash_entries",
    "whole_image",
    "missing_intervals",
    "clock_drift_seconds",
    "clock_correction",
    "clock_confidence",
    "db",
]


def print_summary(summary: dict[str, Any], out: TextIO | None = None) -> None:
    if out is None:
        out = sys.stdout
    for key in SUMMARY_KEYS:
        if key not in summary:
            continue
        value = summary[key]
        if key == "clock_drift_seconds" and isinstance(value, int):
            value = f"{value:+d}"
        print(f"{key}={value}", file=out)

    for gap in summary.get("gaps", []):
        print(
            f"missing_interval kind={gap['kind']} start={gap['start_utc']} "
            f"end={gap['end_utc']} duration_seconds={gap['duration_seconds']}",
            file=out,
        )
    for mismatch in summary.get("hash_mismatch_details", []):
        print(
            f"hash_mismatch block={mismatch['block_index']} "
            f"offset={mismatch['offset']} expected={mismatch['expected']} "
            f"actual={mismatch['actual']}",
            file=out,
        )
    if "clock_reason" in summary:
        print(f"clock_reason={summary['clock_reason']}", file=out)
    steps = summary.get("steps", {})
    if steps:
        line = " ".join(f"{name}={state}" for name, state in steps.items())
        print(f"steps: {line}", file=out)


def write_json(summary: dict[str, Any], path: str | Path) -> None:
    Path(path).write_text(
        json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8")
