"""SQLite persistence: schema, transactions, leases and state transitions.

Every worker thread opens its own connection (:meth:`Store.connect`).  Claims
use ``BEGIN IMMEDIATE`` so concurrent workers serialise on the write lock and
never claim the same job twice.  The database runs in WAL mode with a busy
timeout; a timeout is surfaced as :class:`StoreLockError` so callers can record
the failure and keep processing other jobs.
"""

from __future__ import annotations

import sqlite3
import threading
import time
from pathlib import Path

from .errors import StoreLockError
from .plan import PlannedJob

PENDING = "pending"
RUNNING = "running"
COMPLETED = "completed"
FAILED = "failed"
STATES = (PENDING, RUNNING, COMPLETED, FAILED)

SCHEMA_VERSION = 1

_SCHEMA = """
CREATE TABLE IF NOT EXISTS jobs (
    task_key         TEXT PRIMARY KEY,
    resolution       TEXT NOT NULL,
    width            INTEGER NOT NULL,
    height           INTEGER NOT NULL,
    bitrate_kbps     INTEGER NOT NULL,
    target_bps       INTEGER NOT NULL,
    preset           TEXT NOT NULL,
    source_sha256    TEXT NOT NULL,
    ffmpeg_version   TEXT NOT NULL,
    frames           INTEGER NOT NULL,
    fps              INTEGER NOT NULL,
    state            TEXT NOT NULL CHECK (state IN
                         ('pending','running','completed','failed')),
    attempts         INTEGER NOT NULL DEFAULT 0,
    lease_owner      TEXT,
    lease_expires_at REAL,
    error            TEXT,
    created_at       REAL NOT NULL,
    updated_at       REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_jobs_state ON jobs(state, task_key);

CREATE TABLE IF NOT EXISTS results (
    task_key        TEXT PRIMARY KEY REFERENCES jobs(task_key),
    actual_bps      INTEGER NOT NULL,
    quality_psnr_db REAL NOT NULL,
    encoded_bytes   INTEGER NOT NULL,
    duration_s      REAL NOT NULL,
    output_path     TEXT NOT NULL,
    measured_at     REAL NOT NULL
);

CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
"""


class Store:
    """Owns the database path and hands out per-thread connections."""

    def __init__(self, path: str | Path, busy_timeout: float = 10.0) -> None:
        self.path = Path(path)
        self.busy_timeout = busy_timeout
        self._local = threading.local()
        self._connections: list[sqlite3.Connection] = []
        self._lock = threading.Lock()

    # -- connections -----------------------------------------------------
    def connect(self) -> sqlite3.Connection:
        conn = getattr(self._local, "conn", None)
        if conn is not None:
            return conn
        self.path.parent.mkdir(parents=True, exist_ok=True)
        conn = sqlite3.connect(
            self.path,
            timeout=self.busy_timeout,
            isolation_level=None,  # explicit transactions
            check_same_thread=False,
        )
        conn.row_factory = sqlite3.Row
        conn.execute("PRAGMA journal_mode=WAL")
        conn.execute("PRAGMA synchronous=NORMAL")
        conn.execute("PRAGMA busy_timeout=%d" % int(self.busy_timeout * 1000))
        conn.execute("PRAGMA foreign_keys=ON")
        with self._lock:
            self._connections.append(conn)
        self._local.conn = conn
        return conn

    def close(self) -> None:
        with self._lock:
            connections, self._connections = self._connections, []
        for conn in connections:
            try:
                conn.close()
            except sqlite3.Error:
                pass
        self._local = threading.local()

    def __enter__(self) -> "Store":
        self.init_schema()
        return self

    def __exit__(self, *exc_info: object) -> None:
        self.close()

    # -- transactions ----------------------------------------------------
    def _write(self, fn):
        conn = self.connect()
        try:
            conn.execute("BEGIN IMMEDIATE")
        except sqlite3.OperationalError as exc:
            raise StoreLockError(f"could not acquire write lock: {exc}") from exc
        try:
            result = fn(conn)
        except sqlite3.OperationalError as exc:
            try:
                conn.execute("ROLLBACK")
            except sqlite3.Error:
                pass
            if "locked" in str(exc).lower() or "busy" in str(exc).lower():
                raise StoreLockError(f"database busy: {exc}") from exc
            raise
        except BaseException:
            try:
                conn.execute("ROLLBACK")
            except sqlite3.Error:
                pass
            raise
        try:
            conn.execute("COMMIT")
        except sqlite3.OperationalError as exc:
            raise StoreLockError(f"commit failed: {exc}") from exc
        return result

    def _read(self, fn):
        conn = self.connect()
        try:
            return fn(conn)
        except sqlite3.OperationalError as exc:
            if "locked" in str(exc).lower() or "busy" in str(exc).lower():
                raise StoreLockError(f"database busy: {exc}") from exc
            raise

    # -- schema ----------------------------------------------------------
    def init_schema(self) -> None:
        def op(conn: sqlite3.Connection):
            conn.executescript(_SCHEMA)
            conn.execute(
                "INSERT OR IGNORE INTO meta(key, value) VALUES('schema_version', ?)",
                (str(SCHEMA_VERSION),),
            )

        self._write(op)

    def get_meta(self, key: str) -> str | None:
        row = self._read(
            lambda c: c.execute("SELECT value FROM meta WHERE key=?", (key,)).fetchone()
        )
        return None if row is None else str(row["value"])

    def set_meta(self, key: str, value: str) -> None:
        self._write(
            lambda c: c.execute(
                "INSERT INTO meta(key, value) VALUES(?, ?) "
                "ON CONFLICT(key) DO UPDATE SET value=excluded.value",
                (key, value),
            )
        )
