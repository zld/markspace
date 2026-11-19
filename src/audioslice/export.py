"""Batch JSONL export of previously stored detection results.

Exporting reads regions straight back from SQLite; the detector's boundary
logic lives only in :mod:`audioslice.detector` and is never duplicated here.
One JSON object is written per line, ordered by task id then region order.
"""

from __future__ import annotations

import json
import sqlite3
from pathlib import Path

from .model import Region
from .store import TaskRecord, load_task


def _region_record(task: TaskRecord, kind: str, ordinal: int, region: Region) -> dict:
    return {
        "task_id": task.task_id,
        "input_path": task.input_path,
        "sample_rate": task.sample_rate,
        "type": kind,
        "ordinal": ordinal,
        "start_sample": region.start_sample,
        "end_sample": region.end_sample,
        "length_samples": region.length_samples,
        "start_seconds": region.start_sample / task.sample_rate,
        "end_seconds": region.end_sample / task.sample_rate,
    }


def export_tasks(conn: sqlite3.Connection, task_ids: list[int], out_path: str) -> int:
    """Write regions of every task in ``task_ids`` to ``out_path`` as JSONL.

    Returns the number of JSON lines emitted.  Raises ``KeyError`` if a task id
    is missing so the caller can report it rather than silently exporting less
    than requested.
    """
    destination = Path(out_path)
    if destination.parent and not destination.parent.exists():
        destination.parent.mkdir(parents=True, exist_ok=True)

    lines = 0
    with destination.open("w", encoding="utf-8") as handle:
        for task_id in task_ids:
            task = load_task(conn, task_id)
            if task is None:
                raise KeyError(f"task id {task_id} does not exist in the database")
            for ordinal, region in enumerate(task.segments):
                handle.write(json.dumps(_region_record(task, "segment", ordinal, region)) + "\n")
                lines += 1
            for ordinal, region in enumerate(task.silences):
                handle.write(json.dumps(_region_record(task, "silence", ordinal, region)) + "\n")
                lines += 1
    return lines
