"""SQLite persistence for digests, extraction summaries and validations.

Writes are transactional: a probe that failed fatally is stored with its
diagnostics but never with a partial track list, and any failure rolls the
whole run back so a half-written success record cannot survive.
"""

from __future__ import annotations

import json
import sqlite3
from contextlib import contextmanager
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Iterator

from .model import MediaFile, ValidationResult

SCHEMA_VERSION = 1

SCHEMA = """
CREATE TABLE IF NOT EXISTS files (
    sha256       TEXT PRIMARY KEY,
    path         TEXT NOT NULL,
    size         INTEGER NOT NULL,
    container    TEXT,
    duration_ms  REAL,
    fatal        INTEGER NOT NULL,
    probed_at    TEXT NOT NULL,
    media_json   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS files_path_idx ON files(path);

CREATE TABLE IF NOT EXISTS tracks (
    sha256       TEXT NOT NULL,
    track_id     INTEGER NOT NULL,
    kind         TEXT NOT NULL,
    codec        TEXT NOT NULL,
    time_base    TEXT NOT NULL,
    duration_ms  REAL,
    offset       INTEGER,
    detail_json  TEXT NOT NULL,
    PRIMARY KEY (sha256, track_id, kind),
    FOREIGN KEY (sha256) REFERENCES files(sha256) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS validations (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    baseline_sha  TEXT NOT NULL,
    candidate_sha TEXT NOT NULL,
    tolerance_ms  REAL NOT NULL,
    status        TEXT NOT NULL,
    mismatch_count INTEGER NOT NULL,
    created_at    TEXT NOT NULL,
    result_json   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS validations_pair_idx
    ON validations(baseline_sha, candidate_sha);

CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
"""


class StoreError(Exception):
    """Database cannot be opened, written, or trusted."""


def _now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


class Store:
    """Thin, explicit wrapper around one SQLite connection."""

    def __init__(self, path: str | Path):
        self.path = Path(path)
        self._conn: sqlite3.Connection | None = None

    # -- lifecycle -----------------------------------------------------
    def open(self) -> "Store":
        parent = self.path.parent
        if str(parent) and not parent.exists():
            raise StoreError(f"database directory does not exist: {parent}")
        if parent.exists() and not parent.is_dir():
            raise StoreError(f"database parent is not a directory: {parent}")
        try:
            self._conn = sqlite3.connect(str(self.path))
            self._conn.row_factory = sqlite3.Row
            self._conn.execute("PRAGMA foreign_keys = ON")
            self._conn.executescript(SCHEMA)
            self._conn.execute(
                "INSERT OR REPLACE INTO meta(key, value) VALUES('schema_version', ?)",
                (str(SCHEMA_VERSION),),
            )
            self._conn.commit()
        except sqlite3.DatabaseError as exc:
            self.close()
            raise StoreError(
                f"cannot open database {self.path}: {exc}") from exc
        except OSError as exc:
            self.close()
            raise StoreError(
                f"cannot create database {self.path}: {exc}") from exc
        return self

    def close(self) -> None:
        if self._conn is not None:
            try:
                self._conn.close()
            finally:
                self._conn = None

    def __enter__(self) -> "Store":
        return self.open()

    def __exit__(self, *_exc) -> None:
        self.close()

    @property
    def conn(self) -> sqlite3.Connection:
        if self._conn is None:
            raise StoreError("database is not open")
        return self._conn

    @contextmanager
    def transaction(self) -> Iterator[sqlite3.Connection]:
        conn = self.conn
        try:
            conn.execute("BEGIN IMMEDIATE")
        except sqlite3.DatabaseError as exc:
            raise StoreError(f"cannot begin transaction: {exc}") from exc
        try:
            yield conn
        except BaseException:
            conn.rollback()
            raise
        else:
            conn.commit()

    # -- writes --------------------------------------------------------
    def save_media(self, media: MediaFile) -> str:
        """Insert or update one probed file and its tracks atomically."""
        if not media.sha256:
            raise StoreError("refusing to store a file without a SHA-256")
        payload = json.dumps(media.to_dict(), sort_keys=True)
        with self.transaction() as conn:
            conn.execute(
                """INSERT INTO files
                   (sha256, path, size, container, duration_ms, fatal,
                    probed_at, media_json)
                   VALUES (?, ?, ?, ?, ?, ?, ?, ?)
                   ON CONFLICT(sha256) DO UPDATE SET
                     path=excluded.path,
                     size=excluded.size,
                     container=excluded.container,
                     duration_ms=excluded.duration_ms,
                     fatal=excluded.fatal,
                     probed_at=excluded.probed_at,
                     media_json=excluded.media_json""",
                (media.sha256, media.path, media.size, media.container,
                 media.duration_ms, int(media.fatal), _now(), payload),
            )
            conn.execute("DELETE FROM tracks WHERE sha256 = ?",
                         (media.sha256,))
            if not media.fatal:
                for track in media.tracks:
                    conn.execute(
                        """INSERT INTO tracks
                           (sha256, track_id, kind, codec, time_base,
                            duration_ms, offset, detail_json)
                           VALUES (?, ?, ?, ?, ?, ?, ?, ?)""",
                        (media.sha256, track.track_id, track.kind, track.codec,
                         track.time_base, track.duration_ms, track.offset,
                         json.dumps(track.codec_detail, sort_keys=True)),
                    )
        return media.sha256

    def save_validation(self, result: ValidationResult) -> int:
        """Record a comparison and return its row id."""
        baseline_sha = result.baseline.sha256 or ""
        candidate_sha = result.candidate.sha256 or ""
        payload = json.dumps(result.to_dict(), sort_keys=True)
        with self.transaction() as conn:
            cursor = conn.execute(
                """INSERT INTO validations
                   (baseline_sha, candidate_sha, tolerance_ms, status,
                    mismatch_count, created_at, result_json)
                   VALUES (?, ?, ?, ?, ?, ?, ?)""",
                (baseline_sha, candidate_sha, result.tolerance_ms,
                 result.status, len(result.mismatches), _now(), payload),
            )
        return int(cursor.lastrowid)

    # -- reads ---------------------------------------------------------
    def get_file(self, sha256: str) -> dict[str, Any] | None:
        row = self.conn.execute(
            "SELECT * FROM files WHERE sha256 = ?", (sha256,)).fetchone()
        if row is None:
            return None
        record = dict(row)
        record["tracks"] = [dict(r) for r in self.conn.execute(
            "SELECT * FROM tracks WHERE sha256 = ? ORDER BY track_id",
            (sha256,))]
        return record

    def count_files(self) -> int:
        return int(self.conn.execute(
            "SELECT COUNT(*) FROM files").fetchone()[0])

    def count_validations(self) -> int:
        return int(self.conn.execute(
            "SELECT COUNT(*) FROM validations").fetchone()[0])

    def integrity_check(self) -> None:
        try:
            row = self.conn.execute("PRAGMA integrity_check").fetchone()
        except sqlite3.DatabaseError as exc:
            raise StoreError(f"database integrity check failed: {exc}") from exc
        if row is None or row[0] != "ok":
            raise StoreError(
                f"database integrity check reported {row[0] if row else 'nothing'}")


def open_store(path: str | Path) -> Store:
    return Store(path).open()
