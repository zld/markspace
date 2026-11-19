"""SQLite persistence for scan runs, file jobs, and segments."""
from __future__ import annotations

import json
import sqlite3
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Iterable

from .merge import Segment

_SCHEMA = """
CREATE TABLE IF NOT EXISTS runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    params_json TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE TABLE IF NOT EXISTS jobs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    input_path TEXT NOT NULL,
    file_hash TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('ok', 'error')),
    error TEXT,
    sample_rate INTEGER,
    channels INTEGER,
    total_samples INTEGER,
    noise_floor_db REAL,
    segment_count INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS jobs_identity ON jobs (input_path, file_hash);
CREATE INDEX IF NOT EXISTS jobs_run ON jobs (run_id);
CREATE TABLE IF NOT EXISTS segments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    start_sample INTEGER NOT NULL CHECK (start_sample >= 0),
    end_sample INTEGER NOT NULL CHECK (end_sample > start_sample),
    peak REAL NOT NULL,
    rms REAL NOT NULL,
    noise_floor_db REAL NOT NULL,
    duration_ms REAL NOT NULL,
    UNIQUE (job_id, ordinal)
);
"""


class StoreError(RuntimeError):
    """SQLite configuration, transaction, or write failure."""


@dataclass(frozen=True)
class JobMeta:
    sample_rate: int
    channels: int
    total_samples: int
    noise_floor_db: float


@dataclass(frozen=True)
class StoredJob:
    job_id: int
    input_path: str
    file_hash: str
    segment_count: int
    sample_rate: int
    channels: int
    total_samples: int
    noise_floor_db: float


def _dumps(value: dict[str, Any]) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"))


class Store:
    """A single SQLite connection; every file job is one transaction."""

    def __init__(self, path: str | Path) -> None:
        self.path = Path(path)
        self.connection: sqlite3.Connection
        try:
            self.path.parent.mkdir(parents=True, exist_ok=True)
        except OSError as exc:
            raise StoreError(f"cannot create database directory: {exc}") from exc
        try:
            self.connection = sqlite3.connect(
                str(self.path), isolation_level=None, timeout=10.0
            )
            self.connection.row_factory = sqlite3.Row
            self.connection.execute("PRAGMA foreign_keys = ON")
            self.connection.execute("PRAGMA journal_mode = WAL")
            self.connection.executescript(_SCHEMA)
        except sqlite3.Error as exc:
            raise StoreError(f"cannot open database {self.path}: {exc}") from exc

    def __enter__(self) -> "Store":
        return self

    def __exit__(self, *exc_info: object) -> None:
        self.close()

    def close(self) -> None:
        try:
            self.connection.close()
        except sqlite3.Error as exc:
            raise StoreError(f"cannot close database: {exc}") from exc

    def start_run(self, params: dict[str, Any]) -> int:
        try:
            cursor = self.connection.execute(
                "INSERT INTO runs (params_json) VALUES (?)", (_dumps(params),)
            )
            return int(cursor.lastrowid)
        except sqlite3.Error as exc:
            raise StoreError(f"cannot record run parameters: {exc}") from exc

    def find_reusable(self, input_path: str, file_hash: str) -> StoredJob | None:
        try:
            row = self.connection.execute(
                "SELECT id, input_path, file_hash, segment_count, sample_rate, "
                "channels, total_samples, noise_floor_db FROM jobs "
                "WHERE input_path = ? AND file_hash = ? AND status = 'ok' "
                "ORDER BY id DESC LIMIT 1",
                (input_path, file_hash),
            ).fetchone()
        except sqlite3.Error as exc:
            raise StoreError(f"cannot query existing jobs: {exc}") from exc
        if row is None:
            return None
        return StoredJob(
            row["id"],
            row["input_path"],
            row["file_hash"],
            row["segment_count"],
            int(row["sample_rate"]),
            int(row["channels"]),
            int(row["total_samples"]),
            float(row["noise_floor_db"]),
        )

    def load_segments(self, job_id: int) -> list[Segment]:
        try:
            rows = self.connection.execute(
                "SELECT start_sample, end_sample, peak, rms, noise_floor_db, "
                "duration_ms FROM segments WHERE job_id = ? ORDER BY ordinal",
                (job_id,),
            ).fetchall()
        except sqlite3.Error as exc:
            raise StoreError(f"cannot read segments for job {job_id}: {exc}") from exc
        return [
            Segment(
                start_sample=row["start_sample"],
                end_sample=row["end_sample"],
                peak=row["peak"],
                rms=row["rms"],
                noise_floor_db=row["noise_floor_db"],
                duration_ms=row["duration_ms"],
            )
            for row in rows
        ]

    def record_success(
        self,
        run_id: int,
        input_path: str,
        file_hash: str,
        meta: JobMeta,
        segments: Iterable[Segment],
        *,
        replace: bool = False,
    ) -> StoredJob:
        rows = list(segments)
        try:
            self.connection.execute("BEGIN IMMEDIATE")
            if replace:
                # Explicit recompute supersedes the previous identity.
                self.connection.execute(
                    "DELETE FROM jobs WHERE input_path = ? AND file_hash = ?",
                    (input_path, file_hash),
                )
            cursor = self.connection.execute(
                "INSERT INTO jobs (run_id, input_path, file_hash, status, "
                "sample_rate, channels, total_samples, noise_floor_db, "
                "segment_count) VALUES (?, ?, ?, 'ok', ?, ?, ?, ?, ?)",
                (
                    run_id,
                    input_path,
                    file_hash,
                    meta.sample_rate,
                    meta.channels,
                    meta.total_samples,
                    meta.noise_floor_db,
                    len(rows),
                ),
            )
            job_id = int(cursor.lastrowid)
            self.connection.executemany(
                "INSERT INTO segments (job_id, ordinal, start_sample, end_sample, "
                "peak, rms, noise_floor_db, duration_ms) "
                "VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
                [
                    (
                        job_id,
                        ordinal,
                        segment.start_sample,
                        segment.end_sample,
                        segment.peak,
                        segment.rms,
                        segment.noise_floor_db,
                        segment.duration_ms,
                    )
                    for ordinal, segment in enumerate(rows)
                ],
            )
            self.connection.execute("COMMIT")
        except sqlite3.Error as exc:
            _rollback(self.connection)
            raise StoreError(f"cannot store {input_path}: {exc}") from exc
        except BaseException:
            _rollback(self.connection)
            raise
        return StoredJob(
            job_id,
            input_path,
            file_hash,
            len(rows),
            meta.sample_rate,
            meta.channels,
            meta.total_samples,
            meta.noise_floor_db,
        )

    def record_failure(
        self, run_id: int, input_path: str, file_hash: str, error: str
    ) -> None:
        try:
            self.connection.execute("BEGIN IMMEDIATE")
            self.connection.execute(
                "INSERT INTO jobs (run_id, input_path, file_hash, status, error) "
                "VALUES (?, ?, ?, 'error', ?)",
                (run_id, input_path, file_hash, error),
            )
            self.connection.execute("COMMIT")
        except sqlite3.Error as exc:
            _rollback(self.connection)
            raise StoreError(f"cannot record failure for {input_path}: {exc}") from exc

    def run_summary(self, run_id: int) -> dict[str, Any] | None:
        try:
            row = self.connection.execute(
                "SELECT * FROM runs WHERE id = ?", (run_id,)
            ).fetchone()
            if row is None:
                return None
            counts = self.connection.execute(
                "SELECT status, COUNT(*) AS n, COALESCE(SUM(segment_count), 0) AS s "
                "FROM jobs WHERE run_id = ? GROUP BY status",
                (run_id,),
            ).fetchall()
        except sqlite3.Error as exc:
            raise StoreError(f"cannot summarize run: {exc}") from exc
        summary: dict[str, Any] = {
            "run_id": run_id,
            "params": json.loads(row["params_json"]),
            "created_at": row["created_at"],
            "files_ok": 0,
            "files_failed": 0,
            "segments": 0,
        }
        for count in counts:
            if count["status"] == "ok":
                summary["files_ok"] = count["n"]
            else:
                summary["files_failed"] = count["n"]
            summary["segments"] += count["s"]
        return summary

    def latest_run_id(self) -> int | None:
        try:
            row = self.connection.execute(
                "SELECT MAX(id) AS id FROM runs"
            ).fetchone()
        except sqlite3.Error as exc:
            raise StoreError(f"cannot query runs: {exc}") from exc
        return None if row is None or row["id"] is None else int(row["id"])

    def export_run(self, run_id: int) -> list[dict[str, Any]]:
        """Return segment rows ordered stably by path then ordinal."""
        try:
            rows = self.connection.execute(
                "SELECT j.id AS job_id, j.input_path, j.file_hash, j.sample_rate, "
                "j.channels, j.total_samples, j.segment_count, s.ordinal, "
                "s.start_sample, s.end_sample, s.peak, s.rms, s.noise_floor_db, "
                "s.duration_ms FROM segments s "
                "JOIN jobs j ON j.id = s.job_id "
                "JOIN runs r ON r.id = j.run_id "
                "WHERE j.run_id = ? AND j.status = 'ok' "
                "ORDER BY j.input_path, j.id, s.ordinal",
                (run_id,),
            ).fetchall()
        except sqlite3.Error as exc:
            raise StoreError(f"cannot export run {run_id}: {exc}") from exc
        return [dict(row) for row in rows]


def _rollback(conn: sqlite3.Connection) -> None:
    try:
        conn.execute("ROLLBACK")
    except sqlite3.Error:
        pass
