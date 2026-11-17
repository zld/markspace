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
            for statement in _SCHEMA.split(";"):
                if statement.strip():
                    conn.execute(statement)
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
        keys: tuple[str, ...] | None = None,
    ) -> list[sqlite3.Row]:
        """Claim up to ``limit`` pending jobs.

        When ``keys`` is given, only those task keys are eligible; this keeps a
        scan from picking up rows left in the database by an unrelated plan.
        """
        moment = time.time() if now is None else now

        def op(conn: sqlite3.Connection):
            conn.execute(
                "UPDATE jobs SET state='pending', lease_owner=NULL, "
                "lease_expires_at=NULL, updated_at=? "
                "WHERE state='running' AND (lease_expires_at IS NULL "
                "OR lease_expires_at <= ?)",
                (moment, moment),
            )
            if keys is None:
                rows = conn.execute(
                    "SELECT * FROM jobs WHERE state='pending' "
                    "ORDER BY task_key LIMIT ?",
                    (limit,),
                ).fetchall()
            elif not keys:
                return []
            else:
                placeholders = ",".join("?" for _ in keys)
                rows = conn.execute(
                    f"SELECT * FROM jobs WHERE state='pending' "
                    f"AND task_key IN ({placeholders}) ORDER BY task_key LIMIT ?",
                    (*keys, limit),
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
            claimed = [row["task_key"] for row in rows]
            if not claimed:
                return []
            placeholders = ",".join("?" for _ in claimed)
            return conn.execute(
                f"SELECT * FROM jobs WHERE task_key IN ({placeholders})",
                claimed,
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
    # -- completion -------------------------------------------------------
    def complete(
        self,
        *,
        task_key: str,
        worker_id: str,
        actual_bps: int,
        quality_psnr_db: float,
        encoded_bytes: int,
        duration_s: float,
        output_path: str,
    ) -> bool:
        moment = time.time()

        def op(conn: sqlite3.Connection):
            row = conn.execute(
                "SELECT state, lease_owner FROM jobs WHERE task_key=?",
                (task_key,),
            ).fetchone()
            if row is None:
                raise StoreLockError(f"unknown task {task_key}")
            if row["state"] == COMPLETED:
                return False
            if row["state"] != RUNNING or row["lease_owner"] != worker_id:
                return False
            conn.execute(
                "INSERT INTO results(task_key, actual_bps, quality_psnr_db, "
                "encoded_bytes, duration_s, output_path, measured_at) "
                "VALUES (?,?,?,?,?,?,?) "
                "ON CONFLICT(task_key) DO UPDATE SET "
                "actual_bps=excluded.actual_bps, "
                "quality_psnr_db=excluded.quality_psnr_db, "
                "encoded_bytes=excluded.encoded_bytes, "
                "duration_s=excluded.duration_s, "
                "output_path=excluded.output_path, "
                "measured_at=excluded.measured_at",
                (
                    task_key,
                    actual_bps,
                    quality_psnr_db,
                    encoded_bytes,
                    duration_s,
                    output_path,
                    moment,
                ),
            )
            conn.execute(
                "UPDATE jobs SET state='completed', lease_owner=NULL, "
                "lease_expires_at=NULL, error=NULL, updated_at=? "
                "WHERE task_key=?",
                (moment, task_key),
            )
            return True

        return bool(self._write(op))

    def fail(self, task_key: str, worker_id: str, error: str) -> bool:
        moment = time.time()

        def op(conn: sqlite3.Connection):
            cursor = conn.execute(
                "UPDATE jobs SET state='failed', lease_owner=NULL, "
                "lease_expires_at=NULL, error=?, updated_at=? "
                "WHERE task_key=? AND state='running' AND lease_owner=?",
                (error, moment, task_key, worker_id),
            )
            return cursor.rowcount > 0

        return bool(self._write(op))

    def requeue_failed(self) -> int:
        moment = time.time()

        def op(conn: sqlite3.Connection):
            cursor = conn.execute(
                "UPDATE jobs SET state='pending', error=NULL, updated_at=? "
                "WHERE state='failed'",
                (moment,),
            )
            return cursor.rowcount

        return int(self._write(op))

    # -- queries ----------------------------------------------------------
    def state_counts(self, keys: tuple[str, ...]) -> dict[str, int]:
        counts = {state: 0 for state in STATES}
        if not keys:
            return counts
        placeholders = ",".join("?" for _ in keys)

        def op(conn: sqlite3.Connection):
            return conn.execute(
                f"SELECT state, COUNT(*) AS n FROM jobs "
                f"WHERE task_key IN ({placeholders}) GROUP BY state",
                keys,
            ).fetchall()

        for row in self._read(op):
            counts[str(row["state"])] = int(row["n"])
        return counts

    def completed_keys(self, keys: tuple[str, ...]) -> set[str]:
        if not keys:
            return set()
        placeholders = ",".join("?" for _ in keys)
        rows = self._read(
            lambda c: c.execute(
                f"SELECT task_key FROM jobs WHERE state='completed' "
                f"AND task_key IN ({placeholders})",
                keys,
            ).fetchall()
        )
        return {str(row["task_key"]) for row in rows}

    def results(self, keys: tuple[str, ...]) -> list[sqlite3.Row]:
        if not keys:
            return []
        placeholders = ",".join("?" for _ in keys)
        return list(
            self._read(
                lambda c: c.execute(
                    f"""
                    SELECT j.task_key, j.resolution, j.width, j.height,
                           j.bitrate_kbps, j.target_bps, j.preset,
                           r.actual_bps, r.quality_psnr_db, r.encoded_bytes,
                           r.duration_s, r.output_path
                    FROM jobs j JOIN results r ON r.task_key = j.task_key
                    WHERE j.task_key IN ({placeholders})
                    ORDER BY j.width, j.height, j.target_bps, j.preset
                    """,
                    keys,
                ).fetchall()
            )
        )

    def failed_jobs(self, keys: tuple[str, ...]) -> list[sqlite3.Row]:
        if not keys:
            return []
        placeholders = ",".join("?" for _ in keys)
        return list(
            self._read(
                lambda c: c.execute(
                    f"SELECT task_key, resolution, bitrate_kbps, preset, error "
                    f"FROM jobs WHERE state='failed' AND task_key IN "
                    f"({placeholders}) ORDER BY resolution, bitrate_kbps, preset",
                    keys,
                ).fetchall()
            )
        )

    def has_rows(self) -> bool:
        row = self._read(lambda c: c.execute("SELECT 1 FROM jobs LIMIT 1").fetchone())
        return row is not None
