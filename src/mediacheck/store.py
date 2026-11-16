"""SQLite persistence for scans, per-file and per-track results.

One scan is one transaction: any write failure rolls the whole batch back
so the database never keeps a half-recorded scan.  Storage failures raise
:class:`StorageError`, which the CLI maps to exit code 3.
"""

from __future__ import annotations

import sqlite3
from dataclasses import dataclass

from .container_probe import ProbeResult


class StorageError(Exception):
    """Raised when the result database cannot be written consistently."""


SCHEMA = """
CREATE TABLE IF NOT EXISTS scans (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at TEXT NOT NULL,
    reference TEXT,
    tolerance_ms INTEGER NOT NULL,
    files_total INTEGER NOT NULL,
    files_ok INTEGER NOT NULL,
    files_failed INTEGER NOT NULL,
    track_count INTEGER NOT NULL,
    max_duration_delta_ms INTEGER,
    mismatch_count INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS files (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    scan_id INTEGER NOT NULL REFERENCES scans(id),
    path TEXT NOT NULL,
    ok INTEGER NOT NULL,
    container TEXT,
    error TEXT,
    duration_ms INTEGER,
    sha256 TEXT,
    file_size INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS tracks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id INTEGER NOT NULL REFERENCES files(id),
    track_id INTEGER,
    track_type TEXT,
    codec TEXT,
    raw_codec TEXT,
    duration_ms INTEGER,
    sample_rate REAL,
    channels INTEGER,
    width INTEGER,
    height INTEGER
);
CREATE INDEX IF NOT EXISTS idx_files_scan ON files(scan_id);
CREATE INDEX IF NOT EXISTS idx_tracks_file ON tracks(file_id);
"""


@dataclass(slots=True)
class ScanRecord:
    scan_id: int
    files_total: int
    files_ok: int
    files_failed: int
    track_count: int
    mismatch_count: int


def connect(db_path: str) -> sqlite3.Connection:
    try:
        conn = sqlite3.connect(db_path)
        conn.executescript(SCHEMA)
        conn.commit()
    except sqlite3.Error as exc:
        raise StorageError(f"cannot open/initialize database: {exc}") from exc
    return conn


def save_scan(conn: sqlite3.Connection, *, started_at: str, reference: str | None,
              tolerance_ms: int, results: list[ProbeResult],
              max_duration_delta_ms: int | None,
              mismatch_count: int) -> ScanRecord:
    """Persist a complete scan atomically; roll back on any failure."""
    files_ok = sum(1 for r in results if r.ok)
    track_count = sum(len(r.tracks) for r in results)
    try:
        with conn:  # context manager commits or rolls back automatically
            cur = conn.execute(
                "INSERT INTO scans (started_at, reference, tolerance_ms, "
                "files_total, files_ok, files_failed, track_count, "
                "max_duration_delta_ms, mismatch_count) "
                "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
                (started_at, reference, tolerance_ms, len(results),
                 files_ok, len(results) - files_ok, track_count,
                 max_duration_delta_ms, mismatch_count))
            scan_id = cur.lastrowid
            for result in results:
                fcur = conn.execute(
                    "INSERT INTO files (scan_id, path, ok, container, error, "
                    "duration_ms, sha256, file_size) "
                    "VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
                    (scan_id, result.path, int(result.ok), result.container,
                     result.error, result.duration_ms, result.sha256,
                     result.file_size))
                file_id = fcur.lastrowid
                for track in result.tracks:
                    conn.execute(
                        "INSERT INTO tracks (file_id, track_id, track_type, "
                        "codec, raw_codec, duration_ms, sample_rate, "
                        "channels, width, height) "
                        "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
                        (file_id, track.track_id, track.track_type,
                         track.codec, track.raw_codec, track.duration_ms,
                         track.sample_rate, track.channels, track.width,
                         track.height))
    except sqlite3.Error as exc:
        raise StorageError(f"failed to write scan results: {exc}") from exc
    return ScanRecord(scan_id, len(results), files_ok,
                      len(results) - files_ok, track_count, mismatch_count)


def history(conn: sqlite3.Connection, limit: int = 20) -> list[dict]:
    """Return the most recent scans, newest first."""
    try:
        rows = conn.execute(
            "SELECT id, started_at, reference, tolerance_ms, files_total, "
            "files_ok, files_failed, track_count, max_duration_delta_ms, "
            "mismatch_count FROM scans ORDER BY id DESC LIMIT ?",
            (limit,)).fetchall()
    except sqlite3.Error as exc:
        raise StorageError(f"cannot read scan history: {exc}") from exc
    keys = ("id", "started_at", "reference", "tolerance_ms", "files_total",
            "files_ok", "files_failed", "track_count",
            "max_duration_delta_ms", "mismatch_count")
    return [dict(zip(keys, row)) for row in rows]
