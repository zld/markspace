"""SQLite-backed cache of extraction results, keyed by content SHA-256.

Each record stores the extractor version and check time alongside the
result. A record is reused only when the extractor version matches; any
extractor change forces a fresh parse on next access.
"""
from __future__ import annotations

import json
import sqlite3
from datetime import datetime, timezone

SCHEMA = """
CREATE TABLE IF NOT EXISTS audits (
    sha256 TEXT PRIMARY KEY,
    extractor_version INTEGER NOT NULL,
    checked_at TEXT NOT NULL,
    path TEXT NOT NULL,
    result_json TEXT NOT NULL
)
"""


class Store:
    def __init__(self, path):
        self.path = str(path)
        self.conn = sqlite3.connect(self.path)
        self.conn.execute(SCHEMA)
        self.conn.commit()

    def lookup(self, sha256: str, extractor_version: int) -> dict | None:
        row = self.conn.execute(
            "SELECT extractor_version, result_json FROM audits "
            "WHERE sha256 = ?", (sha256,)).fetchone()
        if row is None or row[0] != extractor_version:
            return None
        return json.loads(row[1])

    def save(self, sha256: str, path, extractor_version: int,
             result: dict) -> None:
        self.conn.execute(
            "INSERT OR REPLACE INTO audits "
            "(sha256, extractor_version, checked_at, path, result_json) "
            "VALUES (?, ?, ?, ?, ?)",
            (sha256, extractor_version,
             datetime.now(timezone.utc).isoformat(), str(path),
             json.dumps(result, sort_keys=True)))
        self.conn.commit()

    def count(self) -> int:
        row = self.conn.execute("SELECT COUNT(*) FROM audits").fetchone()
        return row[0]

    def close(self) -> None:
        self.conn.close()

    def __enter__(self) -> "Store":
        return self

    def __exit__(self, *exc) -> None:
        self.close()
