"""Task planning, atomic claims, lease recovery and state counts."""

from __future__ import annotations

import sqlite3
import time
from dataclasses import dataclass
from typing import Mapping

from .db import (
    FAILED,
    MAX_ATTEMPTS,
    MAX_ERROR_CHARS,
    PENDING,
    RUNNING,
    STATES,
    StoreBase,
    StoreError,
    utc_stamp,
)


@dataclass(frozen=True)
class Task:
    id: int
    task_key: str
    quant_step: int
    block_size: int
    attempts: int


class TaskOps(StoreBase):
    """Mixin with task lifecycle operations; mixed into :class:`Store`."""

    def sync_tasks(
        self,
        run_id: int,
        task_keys: Mapping[tuple[int, int], str],
        now: float | None = None,
    ) -> int:
        """Insert missing tasks idempotently; return the number of new rows."""
        stamp = utc_stamp(now)
        inserted = 0
        conn = self.connect()
        try:
            with self.transaction(conn):
                for (quant, block), task_key in task_keys.items():
                    cursor = conn.execute(
                        "INSERT OR IGNORE INTO tasks (run_id, task_key, quant_step,"
                        " block_size, state, attempts, created_at, updated_at)"
                        " VALUES (?, ?, ?, ?, ?, 0, ?, ?)",
                        (run_id, task_key, quant, block, PENDING, stamp, stamp),
                    )
                    inserted += cursor.rowcount
            return inserted
        finally:
            conn.close()

    def recover_expired_leases(self, run_id: int, now: float | None = None) -> int:
        """Return running tasks whose lease has expired to the retryable pool."""
        seconds = time.time() if now is None else now
        conn = self.connect()
        try:
            with self.transaction(conn):
                return self._expire_leases(conn, run_id, seconds)
        finally:
            conn.close()

    def _expire_leases(
        self, conn: sqlite3.Connection, run_id: int, seconds: float
    ) -> int:
        cursor = conn.execute(
            "UPDATE tasks SET state = ?, worker = NULL, lease_until = NULL,"
            " last_error = 'lease expired', updated_at = ?"
            " WHERE run_id = ? AND state = ? AND lease_until IS NOT NULL"
            " AND lease_until < ?",
            (FAILED, utc_stamp(seconds), run_id, RUNNING, seconds),
        )
        return cursor.rowcount

    def claim_next(
        self,
        conn: sqlite3.Connection,
        run_id: int,
        worker: str,
        lease_seconds: float = 60.0,
        now: float | None = None,
        max_attempts: int = MAX_ATTEMPTS,
    ) -> Task | None:
        """Atomically claim one pending/retryable task."""
        seconds = time.time() if now is None else now
        with self.transaction(conn):
            self._expire_leases(conn, run_id, seconds)
            row = conn.execute(
                "SELECT id, task_key, quant_step, block_size, attempts FROM tasks"
                " WHERE run_id = ? AND state IN (?, ?) AND attempts < ?"
                " ORDER BY id LIMIT 1",
                (run_id, PENDING, FAILED, max_attempts),
            ).fetchone()
            if row is None:
                return None
            cursor = conn.execute(
                "UPDATE tasks SET state = ?, worker = ?, attempts = attempts + 1,"
                " lease_until = ?, last_error = NULL, updated_at = ?"
                " WHERE id = ? AND state IN (?, ?)",
                (
                    RUNNING,
                    worker,
                    seconds + lease_seconds,
                    utc_stamp(seconds),
                    int(row["id"]),
                    PENDING,
                    FAILED,
                ),
            )
            if cursor.rowcount != 1:
                raise StoreError("task claim lost a race")
            return Task(
                id=int(row["id"]),
                task_key=str(row["task_key"]),
                quant_step=int(row["quant_step"]),
                block_size=int(row["block_size"]),
                attempts=int(row["attempts"]) + 1,
            )

    def fail_task(
        self,
        conn: sqlite3.Connection,
        task: Task,
        error: str,
        now: float | None = None,
    ) -> None:
        """Mark a claimed task failed; it stays retryable until attempts cap."""
        message = " ".join(str(error).split())[:MAX_ERROR_CHARS]
        with self.transaction(conn):
            conn.execute(
                "UPDATE tasks SET state = ?, worker = NULL, lease_until = NULL,"
                " last_error = ?, updated_at = ? WHERE id = ? AND state = ?",
                (FAILED, message, utc_stamp(now), task.id, RUNNING),
            )

    def task_counts(self, run_id: int) -> dict[str, int]:
        conn = self.connect()
        try:
            rows = conn.execute(
                "SELECT state, COUNT(*) AS n FROM tasks WHERE run_id = ?"
                " GROUP BY state",
                (run_id,),
            ).fetchall()
            counts = {state: 0 for state in STATES}
            for row in rows:
                counts[str(row["state"])] = int(row["n"])
            counts["total"] = sum(counts[state] for state in STATES)
            return counts
        finally:
            conn.close()

    def failed_tasks(self, run_id: int) -> list[sqlite3.Row]:
        conn = self.connect()
        try:
            return conn.execute(
                "SELECT task_key, quant_step, block_size, attempts, last_error"
                " FROM tasks WHERE run_id = ? AND state = ? ORDER BY id",
                (run_id, FAILED),
            ).fetchall()
        finally:
            conn.close()

    def incomplete_tasks(self, run_id: int) -> int:
        conn = self.connect()
        try:
            row = conn.execute(
                "SELECT COUNT(*) AS n FROM tasks WHERE run_id = ? AND state != ?",
                (run_id, "done"),
            ).fetchone()
            return int(row["n"])
        finally:
            conn.close()
