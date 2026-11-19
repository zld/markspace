"""SQLite persistence for runs, source files, segments and silences.

All writes happen inside a single transaction per run: a file failure rolls
back only that file's rows, while already-committed files stay intact.
Foreign keys are enforced and every query is parameterised.
"""
from __future__ import annotations

import hashlib
import json
import sqlite3
from pathlib import Path

from . import PARAM_VERSION
from .models import AudioData, DetectParams, DetectionResult

_SCHEMA = """
PRAGMA foreign_keys = ON;
CREATE TABLE IF NOT EXISTS runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    param_version INTEGER NOT NULL,
    params_json TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS files (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    rel_path TEXT NOT NULL,
    content_sha256 TEXT NOT NULL,
    sample_rate INTEGER NOT NULL,
    channels INTEGER NOT NULL,
    sample_width INTEGER NOT NULL,
    total_samples INTEGER NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('ok', 'failed')),
    error TEXT,
    noise_floor_db REAL
);
CREATE TABLE IF NOT EXISTS segments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    idx INTEGER NOT NULL,
    start_sample INTEGER NOT NULL,
    end_sample INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS silences (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    idx INTEGER NOT NULL,
    start_sample INTEGER NOT NULL,
    end_sample INTEGER NOT NULL
);
"""


def sha256_file(path: str | Path) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


class Store:
    """Thin transactional wrapper around one sqlite3 database file."""

    def __init__(self, db_path: str | Path):
        self.db_path = str(db_path)
        self.conn = sqlite3.connect(self.db_path)
        self.conn.execute("PRAGMA foreign_keys = ON")
        self.conn.executescript(_SCHEMA)
        self.conn.commit()

    def close(self) -> None:
        self.conn.close()

    def __enter__(self) -> "Store":
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()

    def start_run(self, params: DetectParams) -> int:
        cur = self.conn.execute(
            "INSERT INTO runs (param_version, params_json) VALUES (?, ?)",
            (PARAM_VERSION, json.dumps(params.as_dict(), sort_keys=True)),
        )
        return int(cur.lastrowid)

    def commit(self) -> None:
        self.conn.commit()

    def rollback(self) -> None:
        self.conn.rollback()

    def record_failure(self, run_id: int, rel_path: str, sha: str, error: str) -> None:
        """A failed file owns no media metadata beyond its identity."""
        self.conn.execute(
            "INSERT INTO files (run_id, rel_path, content_sha256, sample_rate, "
            "channels, sample_width, total_samples, status, error) "
            "VALUES (?, ?, ?, 0, 0, 0, 0, 'failed', ?)",
            (run_id, rel_path, sha, error),
        )

    def record_result(
        self,
        run_id: int,
        rel_path: str,
        sha: str,
        audio: AudioData,
        result: DetectionResult,
    ) -> int:
        cur = self.conn.execute(
            "INSERT INTO files (run_id, rel_path, content_sha256, sample_rate, "
            "channels, sample_width, total_samples, status, noise_floor_db) "
            "VALUES (?, ?, ?, ?, ?, ?, ?, 'ok', ?)",
            (
                run_id,
                rel_path,
                sha,
                audio.sample_rate,
                audio.channels,
                audio.sample_width,
                audio.total_samples,
                result.noise_floor_db,
            ),
        )
        file_id = int(cur.lastrowid)
        self.conn.executemany(
            "INSERT INTO segments (file_id, idx, start_sample, end_sample) "
            "VALUES (?, ?, ?, ?)",
            [
                (file_id, i, s.start_sample, s.end_sample)
                for i, s in enumerate(result.segments)
            ],
        )
        self.conn.executemany(
            "INSERT INTO silences (file_id, idx, start_sample, end_sample) "
            "VALUES (?, ?, ?, ?)",
            [
                (file_id, i, z.start_sample, z.end_sample)
                for i, z in enumerate(result.silences)
            ],
        )
        return file_id

    def run_exists(self, run_id: int) -> bool:
        row = self.conn.execute(
            "SELECT 1 FROM runs WHERE id = ?", (run_id,)
        ).fetchone()
        return row is not None

    def load_run(self, run_id: int) -> dict:
        """Load every stored interval for one run, grouped by relative path."""
        if not self.run_exists(run_id):
            raise KeyError(f"run {run_id} not found in {self.db_path}")
        files = self.conn.execute(
            "SELECT id, rel_path, status, error FROM files WHERE run_id = ? "
            "ORDER BY rel_path",
            (run_id,),
        ).fetchall()
        grouped: dict[str, dict] = {}
        for file_id, rel_path, status, error in files:
            segments = [
                {"start_sample": a, "end_sample": b}
                for _, a, b in self.conn.execute(
                    "SELECT idx, start_sample, end_sample FROM segments "
                    "WHERE file_id = ? ORDER BY idx",
                    (file_id,),
                )
            ]
            silences = [
                {"start_sample": a, "end_sample": b}
                for _, a, b in self.conn.execute(
                    "SELECT idx, start_sample, end_sample FROM silences "
                    "WHERE file_id = ? ORDER BY idx",
                    (file_id,),
                )
            ]
            grouped[rel_path] = {
                "status": status,
                "error": error,
                "segments": segments,
                "silences": silences,
            }
        return grouped

    def latest_run_id(self) -> int:
        row = self.conn.execute("SELECT MAX(id) FROM runs").fetchone()
        if row is None or row[0] is None:
            raise KeyError(f"no runs in {self.db_path}")
        return int(row[0])
