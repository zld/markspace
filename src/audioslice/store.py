"""SQLite persistence for jobs, file summaries and segment results.

A *job* captures one parameter set; identical parameters reuse the same job
row, so re-running the same input digest with the same parameters is an
idempotent update (segments are replaced, never duplicated). Each file is
written in its own transaction: a failure rolls that file back and the error
is then recorded separately, keeping earlier successful files intact.
"""
from __future__ import annotations

import json
import sqlite3
from datetime import datetime, timezone
from pathlib import Path

SCHEMA = """
CREATE TABLE IF NOT EXISTS jobs (
    id INTEGER PRIMARY KEY,
    created_at TEXT NOT NULL,
    params_json TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS files (
    id INTEGER PRIMARY KEY,
    job_id INTEGER NOT NULL REFERENCES jobs(id),
    path TEXT NOT NULL,
    digest TEXT,
    sample_rate INTEGER,
    channels INTEGER,
    num_samples INTEGER,
    status TEXT NOT NULL,
    error TEXT,
    UNIQUE (job_id, path)
);
CREATE TABLE IF NOT EXISTS segments (
    id INTEGER PRIMARY KEY,
    file_id INTEGER NOT NULL REFERENCES files(id),
    idx INTEGER NOT NULL,
    start_sample INTEGER NOT NULL,
    end_sample INTEGER NOT NULL,
    duration_seconds REAL NOT NULL,
    peak REAL NOT NULL,
    avg_rms REAL NOT NULL
);
"""


def _utcnow() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


class Store:
    def __init__(self, db_path):
        self.db_path = Path(db_path)
        if self.db_path.parent and not self.db_path.parent.exists():
            self.db_path.parent.mkdir(parents=True, exist_ok=True)
        self._conn = sqlite3.connect(str(self.db_path))
        self._conn.row_factory = sqlite3.Row
        self._conn.executescript(SCHEMA)

    def close(self) -> None:
        self._conn.close()

    def __enter__(self) -> "Store":
        return self

    def __exit__(self, *exc) -> None:
        self.close()

    def get_or_create_job(self, params: dict) -> int:
        blob = json.dumps(params, sort_keys=True)
        row = self._conn.execute(
            "SELECT id FROM jobs WHERE params_json = ?", (blob,)
        ).fetchone()
        if row:
            return int(row["id"])
        cur = self._conn.execute(
            "INSERT INTO jobs (created_at, params_json) VALUES (?, ?)",
            (_utcnow(), blob),
        )
        self._conn.commit()
        return int(cur.lastrowid)

    def _upsert_file(self, job_id: int, path: str, digest: str | None,
                     info: dict | None, status: str, error: str | None) -> int:
        self._conn.execute(
            """INSERT INTO files (job_id, path, digest, sample_rate, channels,
                                  num_samples, status, error)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?)
               ON CONFLICT (job_id, path) DO UPDATE SET
                   digest = excluded.digest,
                   sample_rate = excluded.sample_rate,
                   channels = excluded.channels,
                   num_samples = excluded.num_samples,
                   status = excluded.status,
                   error = excluded.error""",
            (
                job_id, path, digest,
                (info or {}).get("sample_rate"),
                (info or {}).get("channels"),
                (info or {}).get("num_samples"),
                status, error,
            ),
        )
        row = self._conn.execute(
            "SELECT id FROM files WHERE job_id = ? AND path = ?", (job_id, path)
        ).fetchone()
        file_id = int(row["id"])
        self._conn.execute("DELETE FROM segments WHERE file_id = ?", (file_id,))
        return file_id

    def save_file_result(self, job_id: int, path: str, digest: str,
                         info: dict, segments: list[dict]) -> None:
        """Replace one file's result atomically; rolls back on any failure."""
        with self._conn:  # commits on success, rolls back on exception
            file_id = self._upsert_file(job_id, path, digest, info, "ok", None)
            self._conn.executemany(
                """INSERT INTO segments
                       (file_id, idx, start_sample, end_sample,
                        duration_seconds, peak, avg_rms)
                   VALUES (?, ?, ?, ?, ?, ?, ?)""",
                [
                    (
                        file_id, i, s["start_sample"], s["end_sample"],
                        s["duration_seconds"], s["peak"], s["avg_rms"],
                    )
                    for i, s in enumerate(segments)
                ],
            )

    def save_file_error(self, job_id: int, path: str, digest: str | None,
                        message: str) -> None:
        """Record a file-level failure; stale segments from older runs go."""
        with self._conn:
            self._upsert_file(job_id, path, digest, None, "error", message)

    def fetch_segments(self, job_id: int, path: str) -> list[sqlite3.Row]:
        return self._conn.execute(
            """SELECT s.* FROM segments s
               JOIN files f ON f.id = s.file_id
               WHERE f.job_id = ? AND f.path = ?
               ORDER BY s.idx""",
            (job_id, path),
        ).fetchall()

    def count_rows(self, table: str) -> int:
        row = self._conn.execute(f"SELECT COUNT(*) AS n FROM {table}").fetchone()
        return int(row["n"])
