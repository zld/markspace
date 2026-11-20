"""SQLite persistence for tasks, results and the input fingerprint.

One database file per scan. The ``meta`` table pins the run to an
input+grid fingerprint; ``tasks`` tracks pending/running/done/failed;
``results`` is keyed by param_key so re-runs are idempotent.
"""
from __future__ import annotations

import json
import sqlite3
from datetime import datetime, timezone

SCHEMA = """
CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS tasks (
    param_key  TEXT PRIMARY KEY,
    status     TEXT NOT NULL CHECK (status IN ('pending','running','done','failed')),
    attempts   INTEGER NOT NULL DEFAULT 0,
    error      TEXT,
    updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS results (
    param_key     TEXT PRIMARY KEY,
    stream_hash   TEXT NOT NULL,
    actual_bps    INTEGER NOT NULL,
    quality_score REAL NOT NULL,
    nbytes        INTEGER NOT NULL,
    per_clip      TEXT NOT NULL
);
"""

TASK_STATUSES = ("pending", "running", "done", "failed")


class CorruptDatabaseError(Exception):
    """The database file cannot be opened or fails integrity checks."""


class FingerprintMismatchError(Exception):
    """Existing results were produced with a different input/grid fingerprint."""


def _now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def connect(path: str) -> sqlite3.Connection:
    try:
        conn = sqlite3.connect(path, timeout=30.0)
        conn.row_factory = sqlite3.Row
        conn.execute("PRAGMA journal_mode=WAL")
        conn.execute("PRAGMA foreign_keys=ON")
        rows = conn.execute("PRAGMA integrity_check").fetchall()
        if not rows or rows[0][0] != "ok":
            raise CorruptDatabaseError(f"integrity_check failed: {rows[0][0]}")
        return conn
    except sqlite3.DatabaseError as exc:
        raise CorruptDatabaseError(f"cannot open {path}: {exc}") from exc


def init_schema(conn: sqlite3.Connection) -> None:
    try:
        conn.executescript(SCHEMA)
        conn.commit()
    except sqlite3.DatabaseError as exc:
        raise CorruptDatabaseError(f"schema init failed: {exc}") from exc


def check_fingerprint(conn: sqlite3.Connection, fingerprint: str) -> None:
    """Pin the DB to ``fingerprint``; refuse to mix incompatible results."""
    row = conn.execute(
        "SELECT value FROM meta WHERE key='fingerprint'"
    ).fetchone()
    if row is None:
        conn.execute(
            "INSERT INTO meta(key, value) VALUES ('fingerprint', ?)", (fingerprint,)
        )
        conn.commit()
    elif row["value"] != fingerprint:
        raise FingerprintMismatchError(
            "database fingerprint does not match current seed/grid; "
            "use a fresh --db or restore the original parameters"
        )


def seed_tasks(conn: sqlite3.Connection, param_keys: list[str]) -> int:
    """Insert missing tasks as pending; returns number of new tasks."""
    added = 0
    with conn:
        for key in param_keys:
            cur = conn.execute(
                "INSERT OR IGNORE INTO tasks(param_key, status, updated_at)"
                " VALUES (?, 'pending', ?)",
                (key, _now()),
            )
            added += cur.rowcount
    return added


def reclaim_running(conn: sqlite3.Connection) -> int:
    """Return interrupted 'running' tasks to 'pending'."""
    with conn:
        cur = conn.execute(
            "UPDATE tasks SET status='pending', updated_at=? WHERE status='running'",
            (_now(),),
        )
    return cur.rowcount


def claim_task(conn: sqlite3.Connection) -> str | None:
    """Atomically claim one pending task; returns its param_key or None."""
    try:
        conn.execute("BEGIN IMMEDIATE")
        row = conn.execute(
            "SELECT param_key FROM tasks WHERE status='pending'"
            " ORDER BY param_key LIMIT 1"
        ).fetchone()
        if row is None:
            conn.commit()
            return None
        conn.execute(
            "UPDATE tasks SET status='running', attempts=attempts+1,"
            " updated_at=? WHERE param_key=?",
            (_now(), row["param_key"]),
        )
        conn.commit()
        return row["param_key"]
    except Exception:
        conn.rollback()
        raise


def complete_task(conn: sqlite3.Connection, result: dict) -> None:
    """Idempotently store a result and mark the task done."""
    with conn:
        conn.execute(
            "INSERT INTO results(param_key, stream_hash, actual_bps,"
            " quality_score, nbytes, per_clip)"
            " VALUES (?, ?, ?, ?, ?, ?)"
            " ON CONFLICT(param_key) DO UPDATE SET"
            " stream_hash=excluded.stream_hash,"
            " actual_bps=excluded.actual_bps,"
            " quality_score=excluded.quality_score,"
            " nbytes=excluded.nbytes,"
            " per_clip=excluded.per_clip",
            (
                result["param_key"],
                result["stream_hash"],
                result["actual_bps"],
                result["quality_score"],
                result["nbytes"],
                json.dumps(result["per_clip"], sort_keys=True),
            ),
        )
        conn.execute(
            "UPDATE tasks SET status='done', error=NULL, updated_at=?"
            " WHERE param_key=?",
            (_now(), result["param_key"]),
        )


def fail_task(conn: sqlite3.Connection, param_key: str, reason: str) -> None:
    with conn:
        conn.execute(
            "UPDATE tasks SET status='failed', error=?, updated_at=?"
            " WHERE param_key=?",
            (reason[:500], _now(), param_key),
        )


def task_counts(conn: sqlite3.Connection) -> dict[str, int]:
    rows = conn.execute(
        "SELECT status, COUNT(*) AS n FROM tasks GROUP BY status"
    ).fetchall()
    counts = {s: 0 for s in TASK_STATUSES}
    for row in rows:
        counts[row["status"]] = row["n"]
    counts["total"] = sum(counts.values())
    return counts


def load_results(conn: sqlite3.Connection) -> list[dict]:
    rows = conn.execute(
        "SELECT * FROM results ORDER BY actual_bps, param_key"
    ).fetchall()
    return [
        {
            "param_key": r["param_key"],
            "stream_hash": r["stream_hash"],
            "actual_bps": r["actual_bps"],
            "quality_score": r["quality_score"],
            "nbytes": r["nbytes"],
            "per_clip": json.loads(r["per_clip"]),
        }
        for r in rows
    ]
