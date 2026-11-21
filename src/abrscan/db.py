"""Database schema, path rules and the shared connection base."""

from __future__ import annotations

import sqlite3
import time
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path
from typing import Iterator, Sequence

from .params import ParamError

SCHEMA_VERSION = 1
PENDING = "pending"
RUNNING = "running"
DONE = "done"
FAILED = "failed"
STATES = (PENDING, RUNNING, DONE, FAILED)
DEFAULT_LEASE_SECONDS = 60.0
MAX_ATTEMPTS = 3
MAX_ERROR_CHARS = 500

_SCHEMA = """
CREATE TABLE IF NOT EXISTS runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    fixture TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    width INTEGER NOT NULL,
    height INTEGER NOT NULL,
    frame_count INTEGER NOT NULL,
    fps INTEGER NOT NULL,
    targets TEXT NOT NULL DEFAULT '',
    duplicate_targets TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    UNIQUE (fixture, content_hash)
);
CREATE TABLE IF NOT EXISTS tasks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    task_key TEXT NOT NULL,
    quant_step INTEGER NOT NULL,
    block_size INTEGER NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending','running','done','failed')),
    attempts INTEGER NOT NULL DEFAULT 0,
    worker TEXT,
    lease_until REAL,
    last_error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (run_id, task_key)
);
CREATE INDEX IF NOT EXISTS tasks_run_state ON tasks (run_id, state, id);
CREATE TABLE IF NOT EXISTS measurements (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    task_key TEXT NOT NULL,
    quant_step INTEGER NOT NULL,
    block_size INTEGER NOT NULL,
    encoded_bytes INTEGER NOT NULL CHECK (encoded_bytes > 0),
    actual_kbps REAL NOT NULL CHECK (actual_kbps > 0),
    quality_db REAL NOT NULL CHECK (quality_db >= 0 AND quality_db <= 100),
    payload_sha256 TEXT NOT NULL,
    measured_at TEXT NOT NULL,
    UNIQUE (run_id, task_key)
);
"""


class StoreError(RuntimeError):
    """Raised for database-level failures (mapped to exit code 3)."""


@dataclass(frozen=True)
class RunInfo:
    run_id: int
    fixture: str
    content_hash: str
    width: int
    height: int
    frame_count: int
    fps: int
    targets: tuple[int, ...]
    duplicate_targets: tuple[int, ...]


def utc_stamp(now: float | None = None) -> str:
    seconds = time.time() if now is None else now
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(seconds))


def check_db_path(db_path: str | Path, workspace: str | Path | None = None) -> Path:
    """Resolve the database path and require it to stay inside the workspace."""
    root = Path.cwd() if workspace is None else Path(workspace)
    root = root.resolve()
    resolved = Path(db_path).expanduser().resolve()
    if resolved == root or root not in resolved.parents:
        raise ParamError(f"database path must be inside the workspace: {root}")
    if resolved.suffix not in (".sqlite", ".db", ".sqlite3"):
        raise ParamError("database path must end with .sqlite, .sqlite3 or .db")
    return resolved


def row_to_run(row: sqlite3.Row) -> RunInfo:
    def split(text: str) -> tuple[int, ...]:
        return tuple(int(part) for part in text.split(",") if part)

    return RunInfo(
        run_id=int(row["id"]),
        fixture=str(row["fixture"]),
        content_hash=str(row["content_hash"]),
        width=int(row["width"]),
        height=int(row["height"]),
        frame_count=int(row["frame_count"]),
        fps=int(row["fps"]),
        targets=split(str(row["targets"])),
        duplicate_targets=split(str(row["duplicate_targets"])),
    )


class StoreBase:
    """Owns the database file; each worker obtains its own connection."""

    def __init__(self, path: Path):
        self.path = path
        try:
            self.path.parent.mkdir(parents=True, exist_ok=True)
            with self.connect() as conn:
                conn.executescript(_SCHEMA)
                conn.execute(f"PRAGMA user_version = {SCHEMA_VERSION}")
        except sqlite3.Error as exc:
            raise StoreError(f"cannot initialise database: {exc}") from exc

    def connect(self) -> sqlite3.Connection:
        try:
            conn = sqlite3.connect(self.path, timeout=10.0, isolation_level=None)
            conn.row_factory = sqlite3.Row
            conn.execute("PRAGMA journal_mode = WAL")
            conn.execute("PRAGMA synchronous = NORMAL")
            conn.execute("PRAGMA foreign_keys = ON")
            conn.execute("PRAGMA busy_timeout = 10000")
            return conn
        except sqlite3.Error as exc:
            raise StoreError(f"cannot open database: {exc}") from exc

    @contextmanager
    def transaction(
        self, conn: sqlite3.Connection, immediate: bool = True
    ) -> Iterator[sqlite3.Connection]:
        try:
            conn.execute("BEGIN IMMEDIATE" if immediate else "BEGIN")
            yield conn
        except BaseException:
            if conn.in_transaction:
                conn.execute("ROLLBACK")
            raise
        else:
            conn.execute("COMMIT")

    def ensure_run(
        self,
        fixture: str,
        content_hash: str,
        width: int,
        height: int,
        frame_count: int,
        fps: int,
        targets: Sequence[int],
        duplicate_targets: Sequence[int],
        now: float | None = None,
    ) -> tuple[RunInfo, bool]:
        """Create the run once, refresh its target list, return (info, created)."""
        target_text = ",".join(str(value) for value in targets)
        duplicate_text = ",".join(str(value) for value in duplicate_targets)
        conn = self.connect()
        try:
            with self.transaction(conn):
                row = conn.execute(
                    "SELECT id FROM runs WHERE fixture = ? AND content_hash = ?",
                    (fixture, content_hash),
                ).fetchone()
                created = row is None
                if created:
                    cursor = conn.execute(
                        "INSERT INTO runs (fixture, content_hash, width, height,"
                        " frame_count, fps, targets, duplicate_targets, created_at)"
                        " VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
                        (
                            fixture,
                            content_hash,
                            width,
                            height,
                            frame_count,
                            fps,
                            target_text,
                            duplicate_text,
                            utc_stamp(now),
                        ),
                    )
                    run_id = int(cursor.lastrowid)
                else:
                    run_id = int(row["id"])
                    conn.execute(
                        "UPDATE runs SET targets = ?, duplicate_targets = ?"
                        " WHERE id = ?",
                        (target_text, duplicate_text, run_id),
                    )
            return (
                RunInfo(
                    run_id=run_id,
                    fixture=fixture,
                    content_hash=content_hash,
                    width=width,
                    height=height,
                    frame_count=frame_count,
                    fps=fps,
                    targets=tuple(targets),
                    duplicate_targets=tuple(duplicate_targets),
                ),
                created,
            )
        finally:
            conn.close()

    def latest_run(self) -> RunInfo | None:
        conn = self.connect()
        try:
            row = conn.execute(
                "SELECT * FROM runs ORDER BY id DESC LIMIT 1"
            ).fetchone()
            return None if row is None else row_to_run(row)
        finally:
            conn.close()
