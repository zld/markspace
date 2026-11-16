"""SQLite persistence for batches, probe results and migration versions.

Every file is written inside its own savepoint so a failure rolls back only
that file while the rest of the batch survives. Re-runs reuse the batch key
(input root + manifest) and replace its detail rows rather than duplicating
them, which keeps repeat exports stable.
"""

from __future__ import annotations

import json
import os
import sqlite3
from contextlib import contextmanager
from datetime import datetime, timezone
from typing import Any, Iterator

from .probe import MediaInfo, TrackInfo

CURRENT_SCHEMA_VERSION = 1

_SCHEMA = """
CREATE TABLE IF NOT EXISTS meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS batches (
    id INTEGER PRIMARY KEY,
    root TEXT NOT NULL,
    manifest TEXT NOT NULL,
    tolerance_ms INTEGER NOT NULL,
    status TEXT NOT NULL,
    started_at TEXT NOT NULL,
    finished_at TEXT
);
CREATE TABLE IF NOT EXISTS files (
    id INTEGER PRIMARY KEY,
    batch_id INTEGER NOT NULL REFERENCES batches(id),
    name TEXT NOT NULL,
    rel_path TEXT NOT NULL,
    container TEXT NOT NULL,
    byte_size INTEGER NOT NULL,
    duration_ms INTEGER,
    brands TEXT NOT NULL,
    parse_error TEXT,
    error_offset INTEGER,
    probed_at TEXT NOT NULL,
    UNIQUE(batch_id, rel_path)
);
CREATE TABLE IF NOT EXISTS tracks (
    id INTEGER PRIMARY KEY,
    file_id INTEGER NOT NULL REFERENCES files(id),
    track_index INTEGER NOT NULL,
    track_id INTEGER,
    type TEXT NOT NULL,
    codec TEXT NOT NULL,
    codec_family TEXT NOT NULL,
    language TEXT,
    hidden INTEGER NOT NULL,
    params TEXT NOT NULL,
    codec_summary TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS scan_events (
    id INTEGER PRIMARY KEY,
    batch_id INTEGER NOT NULL REFERENCES batches(id),
    rel_path TEXT NOT NULL,
    kind TEXT NOT NULL,
    reason TEXT NOT NULL,
    at TEXT NOT NULL
);
"""


def _now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


class StoreError(Exception):
    """Raised when persistence or migration fails."""


class AuditStore:
    def __init__(self, path: str) -> None:
        self.path = path
        parent = os.path.dirname(os.path.abspath(path))
        if parent and not os.path.isdir(parent):
            raise StoreError(f"database directory does not exist: {parent}")
        self.conn = sqlite3.connect(path)
        self.conn.row_factory = sqlite3.Row
        self.conn.execute("PRAGMA foreign_keys = ON")
        self._migrate()

    def close(self) -> None:
        self.conn.close()

    def __enter__(self) -> "AuditStore":
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()

    # -- migration ----------------------------------------------------------

    def _migrate(self) -> None:
        try:
            self.conn.executescript(_SCHEMA)
            row = self.conn.execute(
                "SELECT value FROM meta WHERE key = 'schema_version'"
            ).fetchone()
            version = int(row["value"]) if row else 0
            while version < CURRENT_SCHEMA_VERSION:
                version += 1
                self._apply_migration(version)
            self.conn.commit()
        except sqlite3.Error as exc:
            self.conn.rollback()
            raise StoreError(f"schema migration failed: {exc}") from exc

    def _apply_migration(self, version: int) -> None:
        # Version 1 is the baseline created by _SCHEMA. Future revisions add
        # explicit ALTER statements here, guarded by the version number.
        if version != 1:
            raise StoreError(f"unknown schema version {version}")
        self.conn.execute(
            "INSERT OR REPLACE INTO meta(key, value) VALUES('schema_version', ?)",
            (str(version),),
        )

    @contextmanager
    def _savepoint(self, name: str) -> Iterator[None]:
        self.conn.execute(f"SAVEPOINT {name}")
        try:
            yield
        except Exception:
            self.conn.execute(f"ROLLBACK TO SAVEPOINT {name}")
            raise
        finally:
            self.conn.execute(f"RELEASE SAVEPOINT {name}")

    # -- batch lifecycle ----------------------------------------------------

    def start_batch(
        self, root: str, manifest: str, tolerance_ms: int
    ) -> int:
        try:
            existing = self.conn.execute(
                "SELECT id FROM batches WHERE root = ? AND manifest = ?",
                (root, manifest),
            ).fetchone()
            if existing:
                batch_id = int(existing["id"])
                self.conn.execute(
                    "DELETE FROM tracks WHERE file_id IN "
                    "(SELECT id FROM files WHERE batch_id = ?)",
                    (batch_id,),
                )
                self.conn.execute(
                    "DELETE FROM files WHERE batch_id = ?", (batch_id,)
                )
                self.conn.execute(
                    "DELETE FROM scan_events WHERE batch_id = ?", (batch_id,)
                )
                self.conn.execute(
                    "UPDATE batches SET tolerance_ms = ?, status = 'running', "
                    "started_at = ?, finished_at = NULL WHERE id = ?",
                    (tolerance_ms, _now(), batch_id),
                )
            else:
                cur = self.conn.execute(
                    "INSERT INTO batches(root, manifest, tolerance_ms, status, "
                    "started_at) VALUES(?, ?, ?, 'running', ?)",
                    (root, manifest, tolerance_ms, _now()),
                )
                batch_id = int(cur.lastrowid)
            self.conn.commit()
            return batch_id
        except sqlite3.Error as exc:
            self.conn.rollback()
            raise StoreError(f"failed to start batch: {exc}") from exc

    def finish_batch(self, batch_id: int, status: str) -> None:
        try:
            self.conn.execute(
                "UPDATE batches SET status = ?, finished_at = ? WHERE id = ?",
                (status, _now(), batch_id),
            )
            self.conn.commit()
        except sqlite3.Error as exc:
            self.conn.rollback()
            raise StoreError(f"failed to finish batch: {exc}") from exc

    def record_event(
        self, batch_id: int, rel_path: str, kind: str, reason: str
    ) -> None:
        with self._savepoint("event"):
            self.conn.execute(
                "INSERT INTO scan_events(batch_id, rel_path, kind, reason, at) "
                "VALUES(?, ?, ?, ?, ?)",
                (batch_id, rel_path, kind, reason, _now()),
            )
        self.conn.commit()

    def save_probe(self, batch_id: int, rel_path: str, info: MediaInfo) -> None:
        """Persist one probe result, rolling back just this file on failure."""
        try:
            with self._savepoint("file"):
                cur = self.conn.execute(
                    "INSERT INTO files(batch_id, name, rel_path, container, "
                    "byte_size, duration_ms, brands, parse_error, error_offset, "
                    "probed_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
                    (
                        batch_id,
                        info.name,
                        rel_path,
                        info.container,
                        info.byte_size,
                        info.duration_ms,
                        json.dumps(list(info.brands)),
                        info.parse_error,
                        info.error_offset,
                        _now(),
                    ),
                )
                file_id = int(cur.lastrowid)
                for index, track in enumerate(info.tracks):
                    self.conn.execute(
                        "INSERT INTO tracks(file_id, track_index, track_id, type, "
                        "codec, codec_family, language, hidden, params, "
                        "codec_summary) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
                        (
                            file_id,
                            index,
                            track.track_id,
                            track.type,
                            track.codec,
                            track.codec_family,
                            track.language,
                            int(track.hidden),
                            json.dumps(track.params, sort_keys=True),
                            track.codec_summary,
                        ),
                    )
            self.conn.commit()
        except sqlite3.Error as exc:
            self.conn.rollback()
            raise StoreError(
                f"failed to persist {rel_path}: {exc}"
            ) from exc
