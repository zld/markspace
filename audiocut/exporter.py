"""Export stored run results as JSONL.

One line per file, read-only: the exporter never re-runs detection.
Failed files carry their error summary so downstream tooling can
trace exactly which inputs did not make it through.
"""
from __future__ import annotations

import json
from typing import IO, List, Optional

from .store import Store, StoreError


def resolve_run_id(store: Store, run_id) -> int:
    """Resolve 'latest' or a numeric run id to an existing run."""
    if run_id == "latest":
        resolved = store.latest_run_id()
        if resolved is None:
            raise StoreError("no runs found in database")
        return resolved
    try:
        resolved = int(run_id)
    except (TypeError, ValueError):
        raise StoreError(f"invalid run id: {run_id!r}")
    if store.get_run(resolved) is None:
        raise StoreError(f"run {resolved} not found in database")
    return resolved


def file_record_to_json(record: dict) -> dict:
    """Shape one stored file row into its JSONL object."""
    obj = {
        "path": record["path"],
        "status": record["status"],
        "sample_rate": record["sample_rate"],
        "channels": record["channels"],
        "sample_frames": record["sample_frames"],
        "clip_count": record["clip_count"],
        "segments": [
            {"start_sample": start, "end_sample": end}
            for start, end in record["segments"]
        ],
        "silence_total_seconds": _silence_seconds(record),
        "sha256": record["sha256"],
    }
    if record["status"] != "ok":
        obj["error"] = record["error"] or "unknown error"
    return obj


def _silence_seconds(record: dict) -> float:
    rate = record["sample_rate"]
    if not rate:
        return 0.0
    return round(record["silence_frames"] / rate, 6)


def export_jsonl(store: Store, run_id, out: IO[str]) -> int:
    """Write one JSON object per file of the run; returns line count."""
    resolved = resolve_run_id(store, run_id)
    records = store.files_of_run(resolved)
    for record in records:
        out.write(json.dumps(file_record_to_json(record), sort_keys=True))
        out.write("\n")
    return len(records)
