"""SQLite persistence for analysis tasks and their detected regions.

Each ``analyze`` invocation creates an independent ``tasks`` row; repeating an
input never overwrites prior results.  A task together with all of its segments
and silences is written inside one explicit transaction, so any failure rolls
the whole task back and never leaves partial rows behind.
"""

from __future__ import annotations

import json
import sqlite3
from dataclasses import dataclass
from pathlib import Path

from .model import AnalysisParams, DetectionResult, Region

_SCHEMA = """
CREATE TABLE IF NOT EXISTS tasks (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    input_path          TEXT NOT NULL,
    sample_rate         INTEGER NOT NULL,
    channels            INTEGER NOT NULL,
    sample_width_bytes  INTEGER NOT NULL,
    total_samples       INTEGER NOT NULL,
    frame_samples       INTEGER NOT NULL,
    min_segment_samples INTEGER NOT NULL,
    min_silence_samples INTEGER NOT NULL,
    margin_db           REAL NOT NULL,
    hysteresis_db       REAL NOT NULL,
    noise_percentile    REAL NOT NULL,
    noise_floor_rms     REAL NOT NULL,
    enter_threshold_rms REAL NOT NULL,
    exit_threshold_rms  REAL NOT NULL,
    frame_count         INTEGER NOT NULL,
    segment_count       INTEGER NOT NULL,
    silence_count       INTEGER NOT NULL,
    created_at          TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS segments (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id       INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    ordinal       INTEGER NOT NULL,
    start_sample  INTEGER NOT NULL,
    end_sample    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS silences (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id       INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    ordinal       INTEGER NOT NULL,
    start_sample  INTEGER NOT NULL,
    end_sample    INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_segments_task ON segments(task_id);
CREATE INDEX IF NOT EXISTS idx_silences_task ON silences(task_id);
"""


@dataclass(frozen=True)
class TaskRecord:
    """A task row joined with everything needed to render summaries/exports."""

    task_id: int
    input_path: str
    sample_rate: int
    channels: int
    sample_width_bytes: int
    total_samples: int
    params: dict
    segment_count: int
    silence_count: int
    created_at: str
    segments: tuple[Region, ...]
    silences: tuple[Region, ...]

    def silence_total_samples(self) -> int:
        return sum(r.length_samples for r in self.silences)

    def silence_total_seconds(self) -> int | float:
        # Sum in samples first, convert once: avoids per-region round-off drift.
        return self.silence_total_samples() / self.sample_rate


def connect(db_path: str) -> sqlite3.Connection:
    """Open (creating schema on first use) the database at ``db_path``."""
    path = Path(db_path)
    if path.parent and not path.parent.exists():
        path.parent.mkdir(parents=True, exist_ok=True)
    conn = sqlite3.connect(str(path))
    conn.execute("PRAGMA foreign_keys = ON")
    conn.executescript(_SCHEMA)
    return conn


def save_task(
    conn: sqlite3.Connection,
    input_path: str,
    params: AnalysisParams,
    result: DetectionResult,
    channels: int,
    sample_width_bytes: int,
) -> int:
    """Persist one full analysis atomically; return the new task id."""
    try:
        conn.execute("BEGIN")
        cursor = conn.execute(
            """
            INSERT INTO tasks (
                input_path, sample_rate, channels, sample_width_bytes,
                total_samples, frame_samples, min_segment_samples,
                min_silence_samples, margin_db, hysteresis_db, noise_percentile,
                noise_floor_rms, enter_threshold_rms, exit_threshold_rms,
                frame_count, segment_count, silence_count
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                input_path,
                params.sample_rate,
                channels,
                sample_width_bytes,
                result.total_samples,
                params.frame_samples,
                params.min_segment_samples,
                params.min_silence_samples,
                params.margin_db,
                params.hysteresis_db,
                params.noise_percentile,
                result.noise_floor_rms,
                result.enter_threshold_rms,
                result.exit_threshold_rms,
                result.frame_count,
                len(result.segments),
                len(result.silences),
            ),
        )
        task_id = cursor.lastrowid
        conn.executemany(
            "INSERT INTO segments (task_id, ordinal, start_sample, end_sample) VALUES (?, ?, ?, ?)",
            [(task_id, i, r.start_sample, r.end_sample) for i, r in enumerate(result.segments)],
        )
        conn.executemany(
            "INSERT INTO silences (task_id, ordinal, start_sample, end_sample) VALUES (?, ?, ?, ?)",
            [(task_id, i, r.start_sample, r.end_sample) for i, r in enumerate(result.silences)],
        )
        conn.commit()
    except Exception:
        conn.rollback()
        raise
    return task_id


def list_task_ids(conn: sqlite3.Connection) -> list[int]:
    return [row[0] for row in conn.execute("SELECT id FROM tasks ORDER BY id")]


def latest_task_id(conn: sqlite3.Connection) -> int | None:
    row = conn.execute("SELECT MAX(id) FROM tasks").fetchone()
    return None if row is None or row[0] is None else int(row[0])


def load_task(conn: sqlite3.Connection, task_id: int) -> TaskRecord | None:
    """Read back a complete task, or ``None`` when the id does not exist."""
    row = conn.execute("SELECT * FROM tasks WHERE id = ?", (task_id,)).fetchone()
    if row is None:
        return None
    columns = [desc[0] for desc in conn.execute("SELECT * FROM tasks LIMIT 0").description]
    data = dict(zip(columns, row))
    segments = tuple(
        Region("segment", start, end)
        for _, _, start, end in conn.execute(
            "SELECT id, ordinal, start_sample, end_sample FROM segments "
            "WHERE task_id = ? ORDER BY ordinal",
            (task_id,),
        )
    )
    silences = tuple(
        Region("silence", start, end)
        for _, _, start, end in conn.execute(
            "SELECT id, ordinal, start_sample, end_sample FROM silences "
            "WHERE task_id = ? ORDER BY ordinal",
            (task_id,),
        )
    )
    params = {
        "frame_samples": data["frame_samples"],
        "min_segment_samples": data["min_segment_samples"],
        "min_silence_samples": data["min_silence_samples"],
        "margin_db": data["margin_db"],
        "hysteresis_db": data["hysteresis_db"],
        "noise_percentile": data["noise_percentile"],
    }
    return TaskRecord(
        task_id=data["id"],
        input_path=data["input_path"],
        sample_rate=data["sample_rate"],
        channels=data["channels"],
        sample_width_bytes=data["sample_width_bytes"],
        total_samples=data["total_samples"],
        params=params,
        segment_count=data["segment_count"],
        silence_count=data["silence_count"],
        created_at=data["created_at"],
        segments=segments,
        silences=silences,
    )


def dump_params_json(params: AnalysisParams) -> str:
    """Stable JSON encoding used by exports; kept here so storage owns its shape."""
    return json.dumps(
        {
            "frame_samples": params.frame_samples,
            "min_segment_samples": params.min_segment_samples,
            "min_silence_samples": params.min_silence_samples,
            "margin_db": params.margin_db,
            "hysteresis_db": params.hysteresis_db,
            "noise_percentile": params.noise_percentile,
        },
        sort_keys=True,
    )
