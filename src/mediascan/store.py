"""SQLite scan index with per-file transactional upserts.

One connection per run.  Each file is written in its own savepoint/transaction
so a failure cannot leak partial tracks for another file, and an interrupted
run rolls back to the last committed file.  Re-scanning the same path updates
the existing row and replaces its tracks instead of inserting duplicates.
"""

from __future__ import annotations

import json
import os
import sqlite3
from dataclasses import dataclass
from datetime import datetime, timezone
from typing import Any

from .extract import Record

SCHEMA = """
CREATE TABLE IF NOT EXISTS scans (
    id INTEGER PRIMARY KEY,
    file_key TEXT NOT NULL UNIQUE,
    path TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    duration_ms INTEGER,
    track_count INTEGER NOT NULL,
    status TEXT NOT NULL,
    error_code TEXT,
    scanned_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS tracks (
    id INTEGER PRIMARY KEY,
    scan_id INTEGER NOT NULL REFERENCES scans(id) ON DELETE CASCADE,
    track_index INTEGER NOT NULL,
    type TEXT NOT NULL,
    track_id INTEGER,
    codec TEXT,
    sample_rate INTEGER,
    channels INTEGER,
    bit_depth INTEGER,
    width INTEGER,
    height INTEGER,
    timescale INTEGER,
    duration_ms INTEGER,
    codec_summary TEXT NOT NULL,
    UNIQUE (scan_id, track_index)
);
CREATE INDEX IF NOT EXISTS idx_tracks_scan ON tracks(scan_id);
CREATE INDEX IF NOT EXISTS idx_scans_path ON scans(path);
"""


class StoreError(RuntimeError):
    pass


@dataclass
class StoredScan:
    path: str
    sha256: str
    action: str  # "inserted" | "updated"


class Store:
    def __init__(self, db_path: str) -> None:
        self.db_path = db_path
        parent = os.path.dirname(os.path.abspath(db_path))
        if parent:
            os.makedirs(parent, exist_ok=True)
        try:
            self.conn = sqlite3.connect(db_path)
            self.conn.row_factory = sqlite3.Row
            self.conn.execute("PRAGMA foreign_keys = ON")
            self.conn.executescript(SCHEMA)
            self.conn.commit()
        except sqlite3.Error as exc:
            raise StoreError(f"cannot open index {db_path}: {exc}") from exc

    def close(self) -> None:
        self.conn.close()

    def __enter__(self) -> "Store":
        return self

    def __exit__(self, *exc_info: Any) -> None:
        self.close()

    def _begin(self) -> None:
        self.conn.execute("BEGIN IMMEDIATE")

    def _commit(self) -> None:
        self.conn.commit()

    def _rollback(self) -> None:
        self.conn.rollback()

    def record_scan(
        self, file_key: str, record: Record, status: str = "ok"
    ) -> StoredScan:
        """Insert or update one successful scan atomically."""
        now = datetime.now(timezone.utc).isoformat(timespec="seconds")
        try:
            self._begin()
            row = self.conn.execute(
                "SELECT id FROM scans WHERE file_key = ?", (file_key,)
            ).fetchone()
            if row is None:
                cur = self.conn.execute(
                    "INSERT INTO scans (file_key, path, sha256, size_bytes,"
                    " duration_ms, track_count, status, error_code, scanned_at)"
                    " VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?)",
                    (
                        file_key,
                        record.path,
                        record.sha256,
                        record.size_bytes,
                        record.duration_ms,
                        len(record.tracks),
                        status,
                        now,
                    ),
                )
                scan_id = int(cur.lastrowid)
                action = "inserted"
            else:
                scan_id = int(row["id"])
                self.conn.execute(
                    "UPDATE scans SET path=?, sha256=?, size_bytes=?, duration_ms=?,"
                    " track_count=?, status=?, error_code=NULL, scanned_at=?"
                    " WHERE id=?",
                    (
                        record.path,
                        record.sha256,
                        record.size_bytes,
                        record.duration_ms,
                        len(record.tracks),
                        status,
                        now,
                        scan_id,
                    ),
                )
                self.conn.execute("DELETE FROM tracks WHERE scan_id = ?", (scan_id,))
                action = "updated"
            self.conn.executemany(
                "INSERT INTO tracks (scan_id, track_index, type, track_id, codec,"
                " sample_rate, channels, bit_depth, width, height, timescale,"
                " duration_ms, codec_summary) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)",
                [
                    (
                        scan_id,
                        t.index,
                        t.type,
                        t.track_id,
                        t.codec,
                        t.sample_rate,
                        t.channels,
                        t.bit_depth,
                        t.width,
                        t.height,
                        t.timescale,
                        t.duration_ms,
                        json.dumps(t.codec_summary(), sort_keys=True),
                    )
                    for t in record.tracks
                ],
            )
            self._commit()
        except sqlite3.Error as exc:
            self._rollback()
            raise StoreError(f"failed to store {record.path}: {exc}") from exc
        return StoredScan(path=record.path, sha256=record.sha256, action=action)

    def record_failure(self, file_key: str, path: str, error_code: str) -> None:
        """Record a failed file without discarding any other file's data."""
        now = datetime.now(timezone.utc).isoformat(timespec="seconds")
        try:
            self._begin()
            self.conn.execute(
                "INSERT INTO scans (file_key, path, sha256, size_bytes, duration_ms,"
                " track_count, status, error_code, scanned_at)"
                " VALUES (?, ?, '', 0, NULL, 0, 'failed', ?, ?)"
                " ON CONFLICT(file_key) DO UPDATE SET path=excluded.path,"
                " sha256='', size_bytes=0, duration_ms=NULL, track_count=0,"
                " status='failed', error_code=excluded.error_code,"
                " scanned_at=excluded.scanned_at",
                (file_key, path, error_code, now),
            )
            self.conn.execute(
                "DELETE FROM tracks WHERE scan_id ="
                " (SELECT id FROM scans WHERE file_key = ?)",
                (file_key,),
            )
            self._commit()
        except sqlite3.Error as exc:
            self._rollback()
            raise StoreError(f"failed to record failure for {path}: {exc}") from exc

    def count_scans(self) -> int:
        row = self.conn.execute("SELECT COUNT(*) AS n FROM scans").fetchone()
        return int(row["n"])

    def fetch(self, file_key: str) -> dict[str, Any] | None:
        row = self.conn.execute(
            "SELECT * FROM scans WHERE file_key = ?", (file_key,)
        ).fetchone()
        if row is None:
            return None
        tracks = self.conn.execute(
            "SELECT * FROM tracks WHERE scan_id = ? ORDER BY track_index",
            (row["id"],),
        ).fetchall()
        data = dict(row)
        data["tracks"] = [dict(t) for t in tracks]
        return data
