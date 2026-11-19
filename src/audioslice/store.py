"""SQLite persistence for detection runs, file summaries and segments.

Schema (three tables):

* ``runs``            - one row per batch invocation with its full config
* ``files``           - per-file hash, status, timings and failure reason
* ``segments``        - every activity/silence interval with coordinates

Write atomicity: a batch opens one explicit transaction and either commits
all of its rows together or rolls everything back, so an interrupted write
can never leave a half-populated batch. Re-importing a file whose content
hash and detector configuration already exist is skipped by the caller via
:meth:`Store.find_existing_file`.
"""

from __future__ import annotations

import sqlite3
from dataclasses import dataclass
from typing import Iterable, Sequence

from .detector import Segment

SCHEMA = """
CREATE TABLE IF NOT EXISTS runs (
    run_id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    config_signature TEXT NOT NULL,
    frame_ms REAL NOT NULL,
    noise_window_ms REAL NOT NULL,
    min_event_ms REAL NOT NULL,
    merge_gap_ms REAL NOT NULL,
    continue_on_error INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS files (
    file_id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL REFERENCES runs(run_id),
    path TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    status TEXT NOT NULL,
    sample_rate INTEGER,
    channels INTEGER,
    n_samples INTEGER,
    duration_seconds REAL,
    silence_total_seconds REAL,
    reason TEXT,
    UNIQUE(run_id, path)
);
CREATE TABLE IF NOT EXISTS segments (
    segment_id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id INTEGER NOT NULL REFERENCES files(file_id),
    kind TEXT NOT NULL,
    start_sample INTEGER NOT NULL,
    end_sample INTEGER NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    candidates TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_files_hash ON files(sha256);
CREATE INDEX IF NOT EXISTS idx_segments_file ON segments(file_id);
"""


class StoreError(RuntimeError):
    """Raised when the database cannot be opened or written."""


@dataclass(frozen=True)
class FileRecord:
    file_id: int
    run_id: int
    path: str
    sha256: str
    status: str


class Store:
    """Thin transactional wrapper around sqlite3."""

    def __init__(self, db_path: str):
        self.db_path = db_path
        try:
            # isolation_level=None -> manual BEGIN/COMMIT, no implicit surprises.
            self._conn = sqlite3.connect(db_path, isolation_level=None)
        except sqlite3.Error as exc:
            raise StoreError(f"cannot open database {db_path}: {exc}") from exc
        self._conn.row_factory = sqlite3.Row
        self._in_tx = False
        try:
            self._conn.executescript(SCHEMA)
        except sqlite3.Error as exc:
            raise StoreError(f"cannot initialise schema in {db_path}: {exc}") from exc

    def close(self) -> None:
        if self._in_tx:
            self.rollback()
        self._conn.close()

    def __enter__(self) -> "Store":
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        self.close()

    # -- transactions -----------------------------------------------------

    def begin(self) -> None:
        if self._in_tx:
            raise StoreError("transaction already open")
        try:
            self._conn.execute("BEGIN IMMEDIATE")
        except sqlite3.Error as e:
            raise StoreError(f"cannot begin transaction: {e}") from e
        self._in_tx = True

    def commit(self) -> None:
        if not self._in_tx:
            raise StoreError("no open transaction")
        self._conn.execute("COMMIT")
        self._in_tx = False

    def rollback(self) -> None:
        if self._in_tx:
            self._conn.execute("ROLLBACK")
            self._in_tx = False

    # -- writes -----------------------------------------------------------

    def create_run(self, config_signature: str, frame_ms: float, noise_window_ms: float,
                   min_event_ms: float, merge_gap_ms: float,
                   continue_on_error: bool) -> int:
        cur = self._conn.execute(
            "INSERT INTO runs (config_signature, frame_ms, noise_window_ms, "
            "min_event_ms, merge_gap_ms, continue_on_error) VALUES (?,?,?,?,?,?)",
            (config_signature, frame_ms, noise_window_ms, min_event_ms, merge_gap_ms,
             1 if continue_on_error else 0),
        )
        return int(cur.lastrowid)

    def find_existing_file(self, sha256: str, config_signature: str) -> str | None:
        """Return the stored path if hash+config were already imported."""
        row = self._conn.execute(
            "SELECT f.path FROM files f JOIN runs r ON f.run_id = r.run_id "
            "WHERE f.sha256 = ? AND r.config_signature = ? AND f.status = 'ok' "
            "LIMIT 1",
            (sha256, config_signature),
        ).fetchone()
        return None if row is None else str(row["path"])

    def add_file(self, run_id: int, path: str, sha256: str, status: str,
                 sample_rate: int | None = None, channels: int | None = None,
                 n_samples: int | None = None, duration_seconds: float | None = None,
                 silence_total_seconds: float | None = None,
                 reason: str | None = None) -> int:
        cur = self._conn.execute(
            "INSERT INTO files (run_id, path, sha256, status, sample_rate, channels, "
            "n_samples, duration_seconds, silence_total_seconds, reason) "
            "VALUES (?,?,?,?,?,?,?,?,?,?)",
            (run_id, path, sha256, status, sample_rate, channels, n_samples,
             duration_seconds, silence_total_seconds, reason),
        )
        return int(cur.lastrowid)

    def add_segments(self, file_id: int, segments: Iterable[Segment]) -> None:
        rows = [
            (file_id, s.kind, int(s.start_sample), int(s.end_sample), s.reason,
             ";".join(f"{a},{b}" for a, b in s.candidates))
            for s in segments
        ]
        self._conn.executemany(
            "INSERT INTO segments (file_id, kind, start_sample, end_sample, reason, candidates) "
            "VALUES (?,?,?,?,?,?)",
            rows,
        )

    # -- reads ------------------------------------------------------------

    def segments_for_file(self, file_id: int) -> Sequence[sqlite3.Row]:
        return self._conn.execute(
            "SELECT kind, start_sample, end_sample, reason, candidates "
            "FROM segments WHERE file_id = ? ORDER BY segment_id", (file_id,)
        ).fetchall()
