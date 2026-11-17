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
    # -- enqueue / resume -------------------------------------------------
    def sync_plan(self, jobs: tuple[PlannedJob, ...]) -> dict[str, int]:
        """Insert missing jobs without disturbing existing state."""
        return self.sync_plan_rows([planned.insert_row() for planned in jobs])

    def sync_plan_rows(self, rows: list[tuple[object, ...]]) -> dict[str, int]:
        def op(conn: sqlite3.Connection):
            now = time.time()
            inserted = existing = 0
            for row in rows:
                cursor = conn.execute(
                    """
                    INSERT INTO jobs (
                        task_key, resolution, width, height, bitrate_kbps,
                        target_bps, preset, source_sha256, ffmpeg_version,
                        frames, fps, state, attempts, created_at, updated_at
                    ) VALUES (?,?,?,?,?,?,?,?,?,?,?,'pending',0,?,?)
                    ON CONFLICT(task_key) DO NOTHING
                    """,
                    (*row, now, now),
                )
                if cursor.rowcount:
                    inserted += 1
                else:
                    existing += 1
            return {"inserted": inserted, "existing": existing}

        return self._write(op)

    # -- leases -----------------------------------------------------------
    def recover_expired(self, now: float | None = None) -> int:
        moment = time.time() if now is None else now

        def op(conn: sqlite3.Connection):
            cursor = conn.execute(
                "UPDATE jobs SET state='pending', lease_owner=NULL, "
                "lease_expires_at=NULL, updated_at=? "
                "WHERE state='running' AND (lease_expires_at IS NULL "
                "OR lease_expires_at <= ?)",
                (moment, moment),
            )
            return cursor.rowcount

        return int(self._write(op))

    def claim(
        self,
        worker_id: str,
        *,
        limit: int,
        lease_seconds: float,
        now: float | None = None,
    ) -> list[sqlite3.Row]:
        moment = time.time() if now is None else now

        def op(conn: sqlite3.Connection):
            conn.execute(
                "UPDATE jobs SET state='pending', lease_owner=NULL, "
                "lease_expires_at=NULL, updated_at=? "
                "WHERE state='running' AND (lease_expires_at IS NULL "
                "OR lease_expires_at <= ?)",
                (moment, moment),
            )
            rows = conn.execute(
                "SELECT * FROM jobs WHERE state='pending' "
                "ORDER BY task_key LIMIT ?",
                (limit,),
            ).fetchall()
            for row in rows:
                conn.execute(
                    "UPDATE jobs SET state='running', lease_owner=?, "
                    "lease_expires_at=?, attempts=attempts+1, error=NULL, "
                    "updated_at=? WHERE task_key=? AND state='pending'",
                    (
                        worker_id,
                        moment + lease_seconds,
                        moment,
                        row["task_key"],
                    ),
                )
            keys = [row["task_key"] for row in rows]
            if not keys:
                return []
            placeholders = ",".join("?" for _ in keys)
            return conn.execute(
                f"SELECT * FROM jobs WHERE task_key IN ({placeholders})",
                keys,
            ).fetchall()

        return list(self._write(op))

    def renew(self, task_key: str, worker_id: str, lease_seconds: float) -> bool:
        moment = time.time()

        def op(conn: sqlite3.Connection):
            cursor = conn.execute(
                "UPDATE jobs SET lease_expires_at=?, updated_at=? "
                "WHERE task_key=? AND state='running' AND lease_owner=?",
                (moment + lease_seconds, moment, task_key, worker_id),
            )
            return cursor.rowcount > 0

        return bool(self._write(op))

    def release(self, task_key: str, worker_id: str, reason: str | None = None) -> bool:
        moment = time.time()

        def op(conn: sqlite3.Connection):
            cursor = conn.execute(
                "UPDATE jobs SET state='pending', lease_owner=NULL, "
                "lease_expires_at=NULL, error=COALESCE(?, error), updated_at=? "
                "WHERE task_key=? AND state='running' AND lease_owner=?",
                (reason, moment, task_key, worker_id),
            )
            return cursor.rowcount > 0

        return bool(self._write(op))

    def release_owned(self, worker_id: str, reason: str | None = None) -> int:
        moment = time.time()

        def op(conn: sqlite3.Connection):
            cursor = conn.execute(
                "UPDATE jobs SET state='pending', lease_owner=NULL, "
                "lease_expires_at=NULL, error=COALESCE(?, error), updated_at=? "
                "WHERE state='running' AND lease_owner=?",
                (reason, moment, worker_id),
            )
            return cursor.rowcount

        return int(self._write(op))
