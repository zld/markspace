"""SQLite persistence for batch runs.

One writer at a time: the batch scheduler is the only code path that
commits, and each file's status row plus all of its segment rows land
in a single transaction, so a crash can never leave half a file's
intervals on disk. Reads (export, dedup lookups) use parameterized
queries only. The schema version is checked on open so a database
written by an incompatible build is rejected instead of misread.
"""
from __future__ import annotations

import sqlite3
from typing import List, Optional

from . import DetectionResult

SCHEMA_VERSION = "1"

_SCHEMA = """
CREATE TABLE IF NOT EXISTS meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runs (
    run_id INTEGER PRIMARY KEY AUTOINCREMENT,
    input_dir TEXT NOT NULL,
    params_json TEXT NOT NULL,
    params_digest TEXT NOT NULL,
    started_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    finished_at TEXT,
    status TEXT NOT NULL DEFAULT 'running'
);
CREATE TABLE IF NOT EXISTS files (
    file_id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL REFERENCES runs(run_id),
    relpath TEXT NOT NULL,
    sha256 TEXT,
    status TEXT NOT NULL,
    error TEXT,
    sample_rate INTEGER,
    channels INTEGER,
    sample_frames INTEGER,
    clip_count INTEGER NOT NULL DEFAULT 0,
    silence_frames INTEGER NOT NULL DEFAULT 0,
    UNIQUE(run_id, relpath)
);
CREATE TABLE IF NOT EXISTS segments (
    file_id INTEGER NOT NULL REFERENCES files(file_id) ON DELETE CASCADE,
    seg_idx INTEGER NOT NULL,
    start_sample INTEGER NOT NULL,
    end_sample INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_segments_file ON segments(file_id);
CREATE INDEX IF NOT EXISTS idx_files_dedup ON files(relpath, sha256);
"""


class StoreError(Exception):
    """Storage-level failure (exit code 1)."""


class Store:
    def __init__(self, path: str):
        try:
            self.conn = sqlite3.connect(path)
            self.conn.execute("PRAGMA foreign_keys = ON")
            self._init_schema()
        except sqlite3.Error as exc:
            raise StoreError(f"cannot open database {path}: {exc}") from exc

    def _init_schema(self) -> None:
        cur = self.conn.execute(
            "SELECT name FROM sqlite_master WHERE type='table' AND name='meta'"
        )
        fresh = cur.fetchone() is None
        if fresh:
            with self.conn:
                self.conn.executescript(_SCHEMA)
                self.conn.execute(
                    "INSERT INTO meta(key, value) VALUES('schema_version', ?)",
                    (SCHEMA_VERSION,),
                )
            return
        row = self.conn.execute(
            "SELECT value FROM meta WHERE key='schema_version'"
        ).fetchone()
        if row is None or row[0] != SCHEMA_VERSION:
            raise StoreError(
                f"unsupported database schema version "
                f"{row[0] if row else None!r}, expected {SCHEMA_VERSION!r}"
            )

    def close(self) -> None:
        self.conn.close()

    # -- runs ------------------------------------------------------------
    def create_run(self, input_dir: str, params_json: str, params_digest: str) -> int:
        with self.conn:
            cur = self.conn.execute(
                "INSERT INTO runs(input_dir, params_json, params_digest) "
                "VALUES (?, ?, ?)",
                (input_dir, params_json, params_digest),
            )
        return int(cur.lastrowid)

    def get_run(self, run_id: int) -> Optional[dict]:
        row = self.conn.execute(
            "SELECT run_id, input_dir, params_json, params_digest, status "
            "FROM runs WHERE run_id = ?",
            (run_id,),
        ).fetchone()
        if row is None:
            return None
        keys = ("run_id", "input_dir", "params_json", "params_digest", "status")
        return dict(zip(keys, row))

    def latest_run_id(self) -> Optional[int]:
        row = self.conn.execute("SELECT MAX(run_id) FROM runs").fetchone()
        return row[0] if row and row[0] is not None else None

    def finish_run(self, run_id: int, status: str = "completed") -> None:
        with self.conn:
            self.conn.execute(
                "UPDATE runs SET status = ?, "
                "finished_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') "
                "WHERE run_id = ?",
                (status, run_id),
            )

    # -- files -----------------------------------------------------------
    def record_file(
        self,
        run_id: int,
        relpath: str,
        sha256: Optional[str],
        status: str,
        error: Optional[str] = None,
        result: Optional[DetectionResult] = None,
    ) -> None:
        """Commit one file's status and all its segments atomically."""
        segments = result.segments if result is not None else []
        try:
            with self.conn:
                cur = self.conn.execute(
                    "INSERT INTO files(run_id, relpath, sha256, status, error, "
                    "sample_rate, channels, sample_frames, clip_count, "
                    "silence_frames) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
                    (
                        run_id,
                        relpath,
                        sha256,
                        status,
                        error,
                        result.sample_rate if result else None,
                        result.channels if result else None,
                        result.sample_frames if result else None,
                        len(segments),
                        result.silence_frames if result else 0,
                    ),
                )
                file_id = cur.lastrowid
                self.conn.executemany(
                    "INSERT INTO segments(file_id, seg_idx, start_sample, "
                    "end_sample) VALUES (?, ?, ?, ?)",
                    [
                        (file_id, i, s.start_sample, s.end_sample)
                        for i, s in enumerate(segments)
                    ],
                )
        except sqlite3.Error as exc:
            raise StoreError(f"failed to record {relpath}: {exc}") from exc

    def find_duplicate(
        self, relpath: str, sha256: str, params_digest: str
    ) -> Optional[dict]:
        """Latest successful result for the same path+content+params."""
        row = self.conn.execute(
            "SELECT f.file_id, f.sample_rate, f.channels, f.sample_frames, "
            "f.clip_count, f.silence_frames "
            "FROM files f JOIN runs r ON f.run_id = r.run_id "
            "WHERE f.relpath = ? AND f.sha256 = ? AND r.params_digest = ? "
            "AND f.status = 'ok' "
            "ORDER BY f.file_id DESC LIMIT 1",
            (relpath, sha256, params_digest),
        ).fetchone()
        if row is None:
            return None
        segments = self._segments_of(row[0])
        return {
            "sample_rate": row[1],
            "channels": row[2],
            "sample_frames": row[3],
            "clip_count": row[4],
            "silence_frames": row[5],
            "segments": segments,
        }

    def _segments_of(self, file_id: int) -> List[tuple]:
        return self.conn.execute(
            "SELECT start_sample, end_sample FROM segments "
            "WHERE file_id = ? ORDER BY seg_idx",
            (file_id,),
        ).fetchall()

    def record_reused(
        self, run_id: int, relpath: str, sha256: str, dup: dict
    ) -> None:
        """Copy a deduplicated result into the current run, one transaction."""
        try:
            with self.conn:
                cur = self.conn.execute(
                    "INSERT INTO files(run_id, relpath, sha256, status, error, "
                    "sample_rate, channels, sample_frames, clip_count, "
                    "silence_frames) VALUES (?, ?, ?, 'ok', NULL, ?, ?, ?, ?, ?)",
                    (
                        run_id,
                        relpath,
                        sha256,
                        dup["sample_rate"],
                        dup["channels"],
                        dup["sample_frames"],
                        dup["clip_count"],
                        dup["silence_frames"],
                    ),
                )
                self.conn.executemany(
                    "INSERT INTO segments(file_id, seg_idx, start_sample, "
                    "end_sample) VALUES (?, ?, ?, ?)",
                    [
                        (cur.lastrowid, i, start, end)
                        for i, (start, end) in enumerate(dup["segments"])
                    ],
                )
        except sqlite3.Error as exc:
            raise StoreError(f"failed to record {relpath}: {exc}") from exc

    def paths_done(self, run_id: int) -> set:
        rows = self.conn.execute(
            "SELECT relpath FROM files WHERE run_id = ?", (run_id,)
        ).fetchall()
        return {r[0] for r in rows}

    def files_of_run(self, run_id: int) -> List[dict]:
        rows = self.conn.execute(
            "SELECT file_id, relpath, status, error, sample_rate, channels, "
            "sample_frames, clip_count, silence_frames, sha256 "
            "FROM files WHERE run_id = ? ORDER BY relpath",
            (run_id,),
        ).fetchall()
        out = []
        for r in rows:
            out.append(
                {
                    "file_id": r[0],
                    "path": r[1],
                    "status": r[2],
                    "error": r[3],
                    "sample_rate": r[4],
                    "channels": r[5],
                    "sample_frames": r[6],
                    "clip_count": r[7],
                    "silence_frames": r[8],
                    "sha256": r[9],
                    "segments": self._segments_of(r[0]),
                }
            )
        return out
