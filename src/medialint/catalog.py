"""SQLite catalog: one writer transaction per scan, queryable history."""
from __future__ import annotations

import json
import sqlite3
from datetime import datetime, timezone
from pathlib import Path

from .model import Inconsistency, StorageError

SCHEMA = """
CREATE TABLE IF NOT EXISTS scans (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    manifest TEXT NOT NULL,
    created_at TEXT NOT NULL,
    tolerance_ms INTEGER NOT NULL,
    status TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS files (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    scan_id INTEGER NOT NULL REFERENCES scans(id),
    path TEXT NOT NULL,
    sha256 TEXT,
    size_bytes INTEGER,
    format TEXT,
    track_count INTEGER NOT NULL,
    ok INTEGER NOT NULL,
    summary TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS issues (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    scan_id INTEGER NOT NULL REFERENCES scans(id),
    path TEXT NOT NULL,
    code TEXT NOT NULL,
    track TEXT,
    message TEXT NOT NULL
);
"""


class Catalog:
    def __init__(self, db_path: str | Path):
        try:
            self._conn = sqlite3.connect(str(db_path))
            self._conn.executescript(SCHEMA)
        except sqlite3.Error as e:
            raise StorageError(f"cannot open catalog {db_path}: {e}")

    def close(self) -> None:
        self._conn.close()

    def record_scan(self, manifest_key: str, tolerance_ms: int,
                    results: list[dict]) -> int:
        """Commit one whole scan atomically. results items carry:
        path, media (dict or None), inconsistencies (list[Inconsistency]).
        A fatal database error rolls back the entire batch."""
        now = datetime.now(timezone.utc).isoformat(timespec="seconds")
        ok = all(not r["inconsistencies"] for r in results)
        try:
            with self._conn:  # commits on success, rolls back on exception
                cur = self._conn.execute(
                    "INSERT INTO scans(manifest, created_at, tolerance_ms, status) VALUES (?,?,?,?)",
                    (manifest_key, now, tolerance_ms, "pass" if ok else "fail"))
                scan_id = cur.lastrowid
                for r in results:
                    media = r["media"]
                    self._conn.execute(
                        "INSERT INTO files(scan_id, path, sha256, size_bytes, format, track_count, ok, summary)"
                        " VALUES (?,?,?,?,?,?,?,?)",
                        (scan_id, r["path"],
                         media["sha256"] if media else None,
                         media["size_bytes"] if media else None,
                         media["format"] if media else None,
                         len(media["tracks"]) if media else 0,
                         0 if r["inconsistencies"] else 1,
                         json.dumps(media, sort_keys=True) if media else "null"))
                    for issue in r["inconsistencies"]:
                        assert isinstance(issue, Inconsistency)
                        self._conn.execute(
                            "INSERT INTO issues(scan_id, path, code, track, message) VALUES (?,?,?,?,?)",
                            (scan_id, r["path"], issue.code, issue.track, issue.message))
        except sqlite3.Error as e:
            raise StorageError(f"catalog write failed, scan rolled back: {e}")
        return scan_id

    def lookup_file(self, manifest_key: str, rel: str) -> dict | None:
        """Latest recorded extraction for one asset of one manifest."""
        try:
            row = self._conn.execute(
                "SELECT f.sha256, f.size_bytes, f.ok, f.summary FROM files f"
                " JOIN scans s ON s.id = f.scan_id"
                " WHERE s.manifest = ? AND f.path = ?"
                " ORDER BY f.id DESC LIMIT 1",
                (manifest_key, rel)).fetchone()
        except sqlite3.Error as e:
            raise StorageError(f"catalog read failed: {e}")
        if row is None:
            return None
        return {"sha256": row[0], "size_bytes": row[1], "ok": row[2],
                "summary": json.loads(row[3])}
