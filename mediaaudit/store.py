"""SQLite persistence for audit and pack runs.

Each run is saved in a single transaction: any failure rolls the run,
its files, tracks and issues back. Files are deduplicated per run by
SHA-256 digest. A schema_version table guards migrations.
"""
from __future__ import annotations

import sqlite3
from datetime import datetime, timezone

SCHEMA_VERSION = 1

SCHEMA = """
CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS runs (
    run_id INTEGER PRIMARY KEY AUTOINCREMENT,
    kind TEXT NOT NULL,
    show_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    status TEXT NOT NULL,
    files INTEGER NOT NULL,
    tracks INTEGER NOT NULL,
    max_duration_delta_ms INTEGER NOT NULL,
    mismatches INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS files (
    file_id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL REFERENCES runs(run_id),
    path TEXT NOT NULL,
    sha256 TEXT,
    container TEXT NOT NULL,
    size INTEGER NOT NULL,
    status TEXT NOT NULL,
    UNIQUE (run_id, sha256)
);
CREATE TABLE IF NOT EXISTS tracks (
    track_id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL REFERENCES runs(run_id),
    file_id INTEGER NOT NULL REFERENCES files(file_id),
    role TEXT NOT NULL,
    type TEXT NOT NULL,
    codec TEXT NOT NULL,
    duration_ms INTEGER NOT NULL,
    width INTEGER,
    height INTEGER,
    frame_rate REAL,
    sample_rate INTEGER,
    channels INTEGER
);
CREATE TABLE IF NOT EXISTS issues (
    issue_id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL REFERENCES runs(run_id),
    path TEXT NOT NULL,
    kind TEXT NOT NULL,
    role TEXT NOT NULL,
    detail TEXT NOT NULL
);
"""


class StoreError(Exception):
    """Fatal database problem (exit code 2)."""


class Store:
    def __init__(self, path: str):
        try:
            self._conn = sqlite3.connect(path)
            self._conn.execute("PRAGMA foreign_keys = ON")
            self.migrate()
        except sqlite3.Error as exc:
            raise StoreError(f"cannot open database {path}: {exc}") from exc

    def close(self):
        self._conn.close()

    def migrate(self):
        try:
            with self._conn:
                self._conn.executescript(SCHEMA)
                row = self._conn.execute(
                    "SELECT version FROM schema_version").fetchone()
                if row is None:
                    self._conn.execute(
                        "INSERT INTO schema_version(version) VALUES (?)",
                        (SCHEMA_VERSION,))
                elif row[0] > SCHEMA_VERSION:
                    raise StoreError(
                        f"database schema {row[0]} is newer than supported "
                        f"version {SCHEMA_VERSION}")
        except sqlite3.Error as exc:
            raise StoreError(f"schema migration failed: {exc}") from exc

    def save_run(self, kind: str, report, records: list) -> int:
        """Persist one run with its files, tracks and issues atomically."""
        now = datetime.now(timezone.utc).isoformat(timespec="seconds")
        try:
            with self._conn:  # commits on success, rolls back on error
                cur = self._conn.execute(
                    "INSERT INTO runs(kind, show_id, created_at, status, "
                    "files, tracks, max_duration_delta_ms, mismatches) "
                    "VALUES (?,?,?,?,?,?,?,?)",
                    (kind, report.show_id, now, report.status, report.files,
                     report.tracks, report.max_duration_delta_ms,
                     len(report.mismatches)))
                run_id = cur.lastrowid
                file_ids = self._insert_files(run_id, records)
                self._insert_tracks(run_id, report.per_file, file_ids)
                self._insert_issues(run_id, report.mismatches)
        except sqlite3.Error as exc:
            raise StoreError(f"failed to save run: {exc}") from exc
        return run_id

    def _insert_files(self, run_id: int, records: list) -> dict:
        file_ids, seen = {}, {}
        for rec in records:
            key = rec.sha256 or f"error:{rec.path}"
            if key in seen:  # SHA-256 dedup: same content stored once
                file_ids[rec.path] = seen[key]
                continue
            cur = self._conn.execute(
                "INSERT INTO files(run_id, path, sha256, container, size, "
                "status) VALUES (?,?,?,?,?,?)",
                (run_id, rec.path, rec.sha256 or None, rec.container,
                 rec.size, "error" if rec.error else "ok"))
            seen[key] = cur.lastrowid
            file_ids[rec.path] = cur.lastrowid
        return file_ids

    def _insert_tracks(self, run_id: int, per_file: list, file_ids: dict):
        for pf in per_file:
            fid = file_ids.get(pf["path"])
            if fid is None:
                continue
            for t in pf["tracks"]:
                self._conn.execute(
                    "INSERT INTO tracks(run_id, file_id, role, type, codec, "
                    "duration_ms, width, height, frame_rate, sample_rate, "
                    "channels) VALUES (?,?,?,?,?,?,?,?,?,?,?)",
                    (run_id, fid, t["role"], t["type"], t["codec"],
                     t["duration_ms"], t["width"], t["height"],
                     t["frame_rate"], t["sample_rate"], t["channels"]))

    def _insert_issues(self, run_id: int, mismatches: list):
        for issue in mismatches:
            self._conn.execute(
                "INSERT INTO issues(run_id, path, kind, role, detail) "
                "VALUES (?,?,?,?,?)",
                (run_id, issue.path, issue.kind, issue.role, issue.detail))
