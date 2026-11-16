"""SQLite-backed task state and result store.

Uses WAL journaling so an abnormal exit never corrupts committed state.
Task statuses: pending -> running -> done | failed. Tasks left in
``running`` by a crash are reclaimed to ``pending`` on startup, and done
artifacts are hash-verified before being trusted.
"""

from __future__ import annotations

import hashlib
import json
import sqlite3
import threading
import time
from pathlib import Path

SCHEMA = """
CREATE TABLE IF NOT EXISTS tasks (
    id INTEGER PRIMARY KEY,
    params_key TEXT NOT NULL UNIQUE,
    source TEXT NOT NULL DEFAULT '',
    width INTEGER NOT NULL,
    height INTEGER NOT NULL,
    target_kbps INTEGER NOT NULL,
    preset TEXT NOT NULL,
    alias_count INTEGER NOT NULL DEFAULT 1,
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    updated_at REAL NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS results (
    task_id INTEGER PRIMARY KEY REFERENCES tasks(id),
    actual_kbps REAL NOT NULL,
    quality_score REAL NOT NULL,
    psnr REAL NOT NULL,
    frames INTEGER NOT NULL,
    duration_s REAL NOT NULL,
    file_hash TEXT NOT NULL,
    file_path TEXT NOT NULL,
    encoder_version TEXT NOT NULL,
    params_json TEXT NOT NULL,
    error TEXT NOT NULL DEFAULT ''
);
"""

STATUSES = ("pending", "running", "done", "failed")


class StoreError(Exception):
    """Database or artifact validation failure (exit code 5)."""


def sha256_file(path: str | Path) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


class Store:
    def __init__(self, path: str | Path):
        self.path = Path(path)
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self._lock = threading.Lock()
        try:
            self._conn = sqlite3.connect(str(self.path), timeout=30, check_same_thread=False)
            self._conn.row_factory = sqlite3.Row
            self._conn.execute("PRAGMA journal_mode=WAL")
            self._conn.execute("PRAGMA foreign_keys=ON")
            self._conn.executescript(SCHEMA)
        except sqlite3.DatabaseError as exc:
            raise StoreError(f"cannot open database {self.path}: {exc}") from exc

    def close(self) -> None:
        with self._lock:
            self._conn.close()

    def _execute(self, sql: str, params: tuple = ()) -> sqlite3.Cursor:
        with self._lock:
            cur = self._conn.execute(sql, params)
            self._conn.commit()
            return cur

    # -- task lifecycle -------------------------------------------------

    def upsert_tasks(self, tasks, source: str) -> None:
        """Insert new tasks; refresh alias_count on existing ones."""
        with self._lock:
            for t in tasks:
                self._conn.execute(
                    """INSERT INTO tasks
                       (params_key, source, width, height, target_kbps, preset,
                        alias_count, status, updated_at)
                       VALUES (?,?,?,?,?,?,?, 'pending', ?)
                       ON CONFLICT(params_key) DO UPDATE SET
                           alias_count=excluded.alias_count,
                           source=excluded.source""",
                    (t.params_key, source, t.width, t.height, t.target_kbps,
                     t.preset, t.alias_count, time.time()),
                )
            self._conn.commit()

    def task_count(self) -> int:
        return self._execute("SELECT COUNT(*) FROM tasks").fetchone()[0]

    def reclaim_stale(self) -> int:
        """Recover tasks orphaned in 'running' by an abnormal exit."""
        cur = self._execute(
            "UPDATE tasks SET status='pending', updated_at=? WHERE status='running'",
            (time.time(),),
        )
        return cur.rowcount

    def due_tasks(self, max_attempts: int) -> list[sqlite3.Row]:
        return self._execute(
            "SELECT * FROM tasks WHERE status='pending' AND attempts<? ORDER BY id",
            (max_attempts,),
        ).fetchall()

    def claim(self, task_id: int, max_attempts: int) -> bool:
        """Atomically move a pending task to running; False if not due."""
        cur = self._execute(
            """UPDATE tasks SET status='running', attempts=attempts+1, updated_at=?
               WHERE id=? AND status='pending' AND attempts<?""",
            (time.time(), task_id, max_attempts),
        )
        return cur.rowcount == 1

    def mark_done(self, task_id: int, result: dict) -> None:
        with self._lock:
            # Reuse an identical artifact instead of keeping a duplicate.
            row = self._conn.execute(
                "SELECT file_path FROM results WHERE file_hash=?", (result["file_hash"],)
            ).fetchone()
            file_path = result["file_path"]
            if row and Path(row[0]).is_file() and row[0] != file_path:
                Path(file_path).unlink(missing_ok=True)
                file_path = row[0]
            self._conn.execute(
                """INSERT INTO results
                   (task_id, actual_kbps, quality_score, psnr, frames, duration_s,
                    file_hash, file_path, encoder_version, params_json, error)
                   VALUES (?,?,?,?,?,?,?,?,?,?, '')
                   ON CONFLICT(task_id) DO UPDATE SET
                       actual_kbps=excluded.actual_kbps,
                       quality_score=excluded.quality_score,
                       psnr=excluded.psnr, frames=excluded.frames,
                       duration_s=excluded.duration_s,
                       file_hash=excluded.file_hash, file_path=excluded.file_path,
                       encoder_version=excluded.encoder_version,
                       params_json=excluded.params_json, error=''""",
                (task_id, result["actual_kbps"], result["quality_score"],
                 result["psnr"], result["frames"], result["duration_s"],
                 result["file_hash"], file_path, result["encoder_version"],
                 json.dumps(result["params"])),
            )
            self._conn.execute(
                "UPDATE tasks SET status='done', updated_at=? WHERE id=?",
                (time.time(), task_id),
            )
            self._conn.commit()

    def finish_failure(self, task_id: int, error: str, max_attempts: int) -> None:
        """Retry as pending while attempts remain, else mark failed."""
        with self._lock:
            attempts = self._conn.execute(
                "SELECT attempts FROM tasks WHERE id=?", (task_id,)
            ).fetchone()[0]
            status = "failed" if attempts >= max_attempts else "pending"
            self._conn.execute(
                "UPDATE tasks SET status=?, updated_at=? WHERE id=?",
                (status, time.time(), task_id),
            )
            self._conn.execute(
                """INSERT INTO results (task_id, actual_kbps, quality_score, psnr,
                       frames, duration_s, file_hash, file_path, encoder_version,
                       params_json, error)
                   VALUES (?,0,0,0,0,0,'','','','{}',?)
                   ON CONFLICT(task_id) DO UPDATE SET error=excluded.error""",
                (task_id, error[:500]),
            )
            self._conn.commit()

    # -- verification and reporting --------------------------------------

    def verify_artifacts(self) -> list[tuple[str, str]]:
        """Hash-check done artifacts; requeue missing/corrupt tasks."""
        issues: list[tuple[str, str]] = []
        try:
            rows = self._execute(
                """SELECT t.id, t.params_key, r.file_hash, r.file_path
                   FROM tasks t JOIN results r ON r.task_id=t.id
                   WHERE t.status='done'"""
            ).fetchall()
        except sqlite3.DatabaseError as exc:
            raise StoreError(f"cannot read results: {exc}") from exc
        for row in rows:
            path = Path(row["file_path"])
            reason = ""
            if not path.is_file():
                reason = "artifact missing"
            elif sha256_file(path) != row["file_hash"]:
                reason = "artifact hash mismatch"
            if reason:
                issues.append((row["params_key"], reason))
                self._execute(
                    "DELETE FROM results WHERE task_id=?", (row["id"],)
                )
                self._execute(
                    """UPDATE tasks SET status='pending', attempts=0, updated_at=?
                       WHERE id=?""",
                    (time.time(), row["id"]),
                )
        return issues

    def counts(self) -> dict[str, int]:
        rows = self._execute(
            "SELECT status, COUNT(*) FROM tasks GROUP BY status"
        ).fetchall()
        out = {s: 0 for s in STATUSES}
        out.update(dict(rows))
        return out

    def ladder_rows(self) -> list[dict]:
        rows = self._execute(
            """SELECT t.target_kbps, t.alias_count, t.params_key,
                      r.actual_kbps, r.quality_score
               FROM tasks t JOIN results r ON r.task_id=t.id
               WHERE t.status='done' ORDER BY t.target_kbps, r.actual_kbps"""
        ).fetchall()
        return [dict(r) for r in rows]
