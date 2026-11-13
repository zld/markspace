"""SQLite persistence: one transaction per run batch, unique batch ids.

Every run stores its input summary, configuration, events, anomalies,
integrity results and gaps atomically; a failure rolls the whole batch back
so history is never partially overwritten.
"""

from __future__ import annotations

import json
import sqlite3
import uuid
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path

from .models import Anomaly, Event, GapInterval, IntegrityResult

SCHEMA = """
CREATE TABLE IF NOT EXISTS batches (
    batch_id    TEXT PRIMARY KEY,
    started_utc TEXT NOT NULL,
    status      TEXT NOT NULL,
    config_json TEXT NOT NULL,
    input_json  TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS events (
    batch_id      TEXT NOT NULL,
    seq           INTEGER NOT NULL,
    event_id      TEXT,
    raw_timestamp TEXT,
    raw_timezone  TEXT,
    naive         INTEGER,
    utc_raw       TEXT,
    utc_final     TEXT,
    corrected     INTEGER,
    confidence    TEXT,
    event_type    TEXT,
    description   TEXT,
    source_block  INTEGER,
    source_offset INTEGER,
    duplicate_of  TEXT,
    PRIMARY KEY (batch_id, seq)
);
CREATE TABLE IF NOT EXISTS anomalies (
    batch_id    TEXT NOT NULL,
    seq         INTEGER NOT NULL,
    kind        TEXT,
    detail      TEXT,
    block_index INTEGER,
    offset      INTEGER,
    PRIMARY KEY (batch_id, seq)
);
CREATE TABLE IF NOT EXISTS integrity (
    batch_id    TEXT NOT NULL,
    seq         INTEGER NOT NULL,
    block_index INTEGER,
    offset      INTEGER,
    expected    TEXT,
    actual      TEXT,
    status      TEXT,
    PRIMARY KEY (batch_id, seq)
);
CREATE TABLE IF NOT EXISTS gaps (
    batch_id         TEXT NOT NULL,
    seq              INTEGER NOT NULL,
    start_utc        TEXT,
    end_utc          TEXT,
    duration_seconds INTEGER,
    kind             TEXT,
    PRIMARY KEY (batch_id, seq)
);
"""


class StoreError(Exception):
    """Generic persistence failure."""


class DatabaseLockedError(StoreError):
    """The database is locked by another process (exit code 4)."""


class DiskFullError(StoreError):
    """The database or its filesystem is full (exit code 5)."""


@dataclass
class BatchData:
    batch_id: str
    started_utc: str
    status: str
    config: dict
    inputs: dict
    events: list[Event] = field(default_factory=list)
    anomalies: list[Anomaly] = field(default_factory=list)
    integrity: list[IntegrityResult] = field(default_factory=list)
    gaps: list[GapInterval] = field(default_factory=list)


def new_batch_id() -> str:
    return uuid.uuid4().hex


def utc_now_iso() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def _map_operational_error(exc: sqlite3.OperationalError) -> StoreError:
    msg = str(exc).lower()
    if "locked" in msg:
        return DatabaseLockedError(str(exc))
    if "full" in msg:
        return DiskFullError(str(exc))
    return StoreError(str(exc))


def _utc(dt: datetime) -> str:
    return dt.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")


def connect(db_path: str | Path) -> sqlite3.Connection:
    p = Path(db_path)
    try:
        if str(p.parent) and not p.parent.exists():
            p.parent.mkdir(parents=True, exist_ok=True)
    except OSError as exc:
        raise StoreError(f"cannot create database directory {p.parent}: {exc}") from exc
    try:
        conn = sqlite3.connect(str(p), timeout=1.0)
        conn.executescript(SCHEMA)
        # Lightweight migration for databases created before the naive column.
        cols = {row[1] for row in conn.execute("PRAGMA table_info(events)")}
        if cols and "naive" not in cols:
            conn.execute("ALTER TABLE events ADD COLUMN naive INTEGER")
            conn.commit()
    except sqlite3.OperationalError as exc:
        raise _map_operational_error(exc) from exc
    return conn


def save_batch(conn: sqlite3.Connection, batch: BatchData) -> None:
    """Persist one batch atomically; any failure rolls the batch back."""
    try:
        with conn:
            conn.execute(
                "INSERT INTO batches VALUES (?, ?, ?, ?, ?)",
                (batch.batch_id, batch.started_utc, batch.status,
                 json.dumps(batch.config, sort_keys=True),
                 json.dumps(batch.inputs, sort_keys=True)),
            )
            conn.executemany(
                "INSERT INTO events VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                [
                    (batch.batch_id, seq, e.event_id, e.raw_timestamp, e.raw_timezone,
                     int(e.naive), _utc(e.utc_raw), _utc(e.utc_final), int(e.corrected),
                     e.confidence.value, e.event_type, e.description,
                     e.source_block, e.source_offset, e.duplicate_of)
                    for seq, e in enumerate(batch.events)
                ],
            )
            conn.executemany(
                "INSERT INTO anomalies VALUES (?,?,?,?,?,?)",
                [
                    (batch.batch_id, seq, a.kind, a.detail, a.block_index, a.offset)
                    for seq, a in enumerate(batch.anomalies)
                ],
            )
            conn.executemany(
                "INSERT INTO integrity VALUES (?,?,?,?,?,?,?)",
                [
                    (batch.batch_id, seq, r.block_index, r.offset, r.expected,
                     r.actual, r.status.value)
                    for seq, r in enumerate(batch.integrity)
                ],
            )
            conn.executemany(
                "INSERT INTO gaps VALUES (?,?,?,?,?,?)",
                [
                    (batch.batch_id, seq, _utc(g.start_utc), _utc(g.end_utc),
                     g.duration_seconds, g.kind)
                    for seq, g in enumerate(batch.gaps)
                ],
            )
    except sqlite3.OperationalError as exc:
        raise _map_operational_error(exc) from exc
    except sqlite3.Error as exc:
        raise StoreError(str(exc)) from exc
