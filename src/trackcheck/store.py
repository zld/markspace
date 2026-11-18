"""sqlite3-backed scan snapshot cache keyed by path and content hash."""
from __future__ import annotations

import json
import sqlite3
from pathlib import Path

from .model import Media

SCHEMA_VERSION = 1


class StoreError(RuntimeError):
    """Raised when the snapshot database is corrupt or unwritable."""


class SnapshotStore:
    """Caches probe results; unchanged files are served from the cache."""

    def __init__(self, db_path: str | Path, rebuild: bool = False):
        self.db_path = Path(db_path)
        try:
            self.db_path.parent.mkdir(parents=True, exist_ok=True)
            if rebuild and self.db_path.exists():
                # SQLite rejects any SQL on a non-database file, so a
                # rebuild must remove the corrupt file before reconnecting.
                self.db_path.unlink()
            self.conn = sqlite3.connect(str(self.db_path))
            self.conn.row_factory = sqlite3.Row
            self._ensure_schema()
        except sqlite3.DatabaseError as exc:
            raise StoreError(f"cannot open store {self.db_path}: {exc}") from exc
        except OSError as exc:
            raise StoreError(f"cannot create store {self.db_path}: {exc}") from exc

    def _ensure_schema(self) -> None:
        try:
            self.conn.execute("PRAGMA user_version")
            self.conn.executescript(
                """
                CREATE TABLE IF NOT EXISTS meta (
                    key TEXT PRIMARY KEY,
                    value TEXT NOT NULL
                );
                CREATE TABLE IF NOT EXISTS snapshots (
                    path TEXT NOT NULL,
                    content_hash TEXT NOT NULL,
                    mtime_ns INTEGER NOT NULL,
                    size INTEGER NOT NULL,
                    media_json TEXT NOT NULL,
                    PRIMARY KEY (path, content_hash)
                );
                """
            )
            self.conn.execute(
                "INSERT OR REPLACE INTO meta(key, value) VALUES('schema_version', ?)",
                (str(SCHEMA_VERSION),),
            )
            self.conn.commit()
        except sqlite3.DatabaseError as exc:
            raise StoreError(f"corrupt store schema: {exc}") from exc

    @staticmethod
    def _key(path: Path) -> str:
        return str(path.resolve())

    def get(self, path: str | Path, content_hash: str) -> Media | None:
        """Return a cached media record if path and hash match exactly."""
        p = Path(path)
        try:
            row = self.conn.execute(
                "SELECT media_json, mtime_ns, size FROM snapshots "
                "WHERE path = ? AND content_hash = ?",
                (self._key(p), content_hash),
            ).fetchone()
        except sqlite3.DatabaseError as exc:
            raise StoreError(f"corrupt store while reading: {exc}") from exc
        if row is None:
            return None
        data = json.loads(row["media_json"])
        return _media_from_dict(data)

    def put(self, media: Media, mtime_ns: int, size: int) -> None:
        try:
            self.conn.execute(
                "INSERT OR REPLACE INTO snapshots "
                "(path, content_hash, mtime_ns, size, media_json) "
                "VALUES (?, ?, ?, ?, ?)",
                (
                    self._key(Path(media.path)),
                    media.content_hash,
                    mtime_ns,
                    size,
                    json.dumps(media.to_dict(), sort_keys=True),
                ),
            )
            self.conn.commit()
        except sqlite3.DatabaseError as exc:
            raise StoreError(f"corrupt store while writing: {exc}") from exc

    def rebuild(self) -> None:
        """Drop and recreate every table, discarding all snapshots."""
        try:
            self.conn.executescript(
                "DROP TABLE IF EXISTS snapshots; DROP TABLE IF EXISTS meta;"
            )
            self._ensure_schema()
            self.conn.commit()
        except sqlite3.DatabaseError as exc:
            raise StoreError(f"failed to rebuild store: {exc}") from exc

    def close(self) -> None:
        self.conn.close()


def _media_from_dict(data: dict) -> Media:
    from .model import Track, TrackError

    tracks = [Track(**{k: v for k, v in t.items() if k != "duration_ms"})
              for t in data.get("tracks", [])]
    errors = [TrackError(**e) for e in data.get("errors", [])]
    return Media(
        path=data["path"],
        version=data["version"],
        header_length=data["header_length"],
        track_count=data["track_count"],
        declared_length=data["declared_length"],
        actual_length=data["actual_length"],
        tracks=tracks,
        errors=errors,
        content_hash=data.get("content_hash", ""),
        unrecognized_fields=data.get("unrecognized_fields", {}),
    )
