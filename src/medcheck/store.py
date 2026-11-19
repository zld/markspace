"""sqlite3 persistence for scan batches, probe results and mismatches.

Each scan opens a batch row. Probe records are cached by SHA-256 plus
parser version: re-running a scan over unchanged files reuses the cached
extraction, and a parser version bump invalidates old entries
automatically. Cached payloads contain metadata only, never media bytes.
"""

from __future__ import annotations

import json
import sqlite3
from datetime import datetime, timezone

_SCHEMA = """
CREATE TABLE IF NOT EXISTS batches (
    id INTEGER PRIMARY KEY,
    created_at TEXT NOT NULL,
    parser_version TEXT NOT NULL,
    tolerance_ms REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS files (
    id INTEGER PRIMARY KEY,
    batch_id INTEGER NOT NULL REFERENCES batches(id),
    path TEXT NOT NULL,
    sha256 TEXT,
    size INTEGER,
    format TEXT,
    track_count INTEGER,
    duration_delta_ms REAL,
    codec_summary TEXT,
    status TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS tracks (
    file_id INTEGER NOT NULL REFERENCES files(id),
    idx INTEGER NOT NULL,
    json TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS mismatches (
    file_id INTEGER NOT NULL REFERENCES files(id),
    json TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS errors (
    file_id INTEGER NOT NULL REFERENCES files(id),
    json TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS probe_cache (
    sha256 TEXT NOT NULL,
    parser_version TEXT NOT NULL,
    payload TEXT NOT NULL,
    PRIMARY KEY (sha256, parser_version)
);
"""


class Store:
    def __init__(self, path: str, parser_version: str):
        self.parser_version = parser_version
        self.conn = sqlite3.connect(path)
        self.conn.executescript(_SCHEMA)

    def begin_batch(self, tolerance_ms: float) -> int:
        cur = self.conn.execute(
            "INSERT INTO batches (created_at, parser_version, tolerance_ms) "
            "VALUES (?, ?, ?)",
            (datetime.now(timezone.utc).isoformat(timespec="seconds"),
             self.parser_version, tolerance_ms))
        self.conn.commit()
        return cur.lastrowid

    def cache_get(self, sha256: str) -> dict | None:
        row = self.conn.execute(
            "SELECT payload FROM probe_cache WHERE sha256 = ? AND parser_version = ?",
            (sha256, self.parser_version)).fetchone()
        return json.loads(row[0]) if row else None

    def cache_put(self, sha256: str, record: dict) -> None:
        self.conn.execute(
            "INSERT OR REPLACE INTO probe_cache (sha256, parser_version, payload) "
            "VALUES (?, ?, ?)",
            (sha256, self.parser_version, json.dumps(record)))
        self.conn.commit()

    def record_file(self, batch_id: int, record: dict, sha256: str | None,
                    size: int | None, mismatches: list[dict],
                    duration_delta_ms: float, codec_summary: str) -> None:
        status = "error" if record.get("errors") else (
            "mismatch" if mismatches else "ok")
        cur = self.conn.execute(
            "INSERT INTO files (batch_id, path, sha256, size, format, "
            "track_count, duration_delta_ms, codec_summary, status) "
            "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
            (batch_id, record.get("path"), sha256, size, record.get("format"),
             len(record.get("tracks", [])), duration_delta_ms, codec_summary,
             status))
        file_id = cur.lastrowid
        for idx, track in enumerate(record.get("tracks", [])):
            self.conn.execute(
                "INSERT INTO tracks (file_id, idx, json) VALUES (?, ?, ?)",
                (file_id, idx, json.dumps(track)))
        for mismatch in mismatches:
            self.conn.execute(
                "INSERT INTO mismatches (file_id, json) VALUES (?, ?)",
                (file_id, json.dumps(mismatch)))
        for error in record.get("errors", []):
            self.conn.execute(
                "INSERT INTO errors (file_id, json) VALUES (?, ?)",
                (file_id, json.dumps(error)))
        self.conn.commit()

    def close(self) -> None:
        self.conn.close()

    def __enter__(self) -> "Store":
        return self

    def __exit__(self, *exc) -> None:
        self.close()
