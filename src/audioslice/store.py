"""Versioned SQLite result cache.

A result row is identified by a cache key derived from the file's absolute
path, size, modification time and the full detector parameter signature:

* re-running the same file with the same parameters hits the cache and does
  not write a second copy;
* changing any parameter produces a new key and therefore a new result set;
* every write happens inside one transaction, so a failure rolls back and
  leaves no half-written segment rows.

All values are passed through parameter binding; paths are treated strictly as
data.  SQLite operational errors (database locked, disk I/O, malformed file)
are re-raised as :class:`~audioslice.errors.StoreError` so the CLI can map
them to exit code 3.
"""

from __future__ import annotations

import hashlib
import json
import os
import sqlite3
from dataclasses import dataclass

from .detector import DetectionResult, Segment
from .errors import StoreError

#: Bump whenever the stored schema or segmentation semantics change.
RESULT_VERSION = 1
_BUSY_TIMEOUT_SECONDS = 5.0


@dataclass(frozen=True)
class FileFingerprint:
    path: str
    size: int
    mtime_ns: int


def fingerprint(path: str) -> FileFingerprint:
    try:
        stat = os.stat(path)
    except OSError as exc:
        raise StoreError(path, f"cannot stat file for cache key: {exc.strerror or exc}") from exc
    return FileFingerprint(os.path.abspath(path), stat.st_size, stat.st_mtime_ns)


def make_cache_key(fp: FileFingerprint, params: dict[str, object]) -> str:
    payload = json.dumps(
        {
            "version": RESULT_VERSION,
            "path": fp.path,
            "size": fp.size,
            "mtime_ns": fp.mtime_ns,
            "params": params,
        },
        sort_keys=True,
        separators=(",", ":"),
    )
    return hashlib.sha256(payload.encode("utf-8")).hexdigest()


class Store:
    """Thin transactional wrapper around a SQLite database."""

    def __init__(self, db_path: str) -> None:
        self.db_path = db_path
        try:
            self._conn = sqlite3.connect(db_path, timeout=_BUSY_TIMEOUT_SECONDS)
        except sqlite3.Error as exc:
            raise StoreError(db_path, f"cannot open database: {exc}") from exc
        self._conn.execute("PRAGMA foreign_keys = ON")
        self._init_schema()

    def _init_schema(self) -> None:
        try:
            with self._conn:
                self._conn.executescript(
                    """
                    CREATE TABLE IF NOT EXISTS results (
                        cache_key TEXT PRIMARY KEY,
                        result_version INTEGER NOT NULL,
                        path TEXT NOT NULL,
                        file_size INTEGER NOT NULL,
                        mtime_ns INTEGER NOT NULL,
                        params_json TEXT NOT NULL,
                        sample_rate INTEGER NOT NULL,
                        total_frames INTEGER NOT NULL,
                        silence_total_seconds REAL NOT NULL,
                        created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
                    );
                    CREATE TABLE IF NOT EXISTS segments (
                        cache_key TEXT NOT NULL,
                        kind TEXT NOT NULL CHECK (kind IN ('segment', 'silence')),
                        ordinal INTEGER NOT NULL,
                        start_sample INTEGER NOT NULL,
                        end_sample INTEGER NOT NULL,
                        PRIMARY KEY (cache_key, kind, ordinal),
                        FOREIGN KEY (cache_key) REFERENCES results(cache_key)
                    );
                    """
                )
        except sqlite3.Error as exc:
            raise StoreError(self.db_path, f"schema initialization failed: {exc}") from exc

    def has_result(self, cache_key: str) -> bool:
        try:
            cur = self._conn.execute(
                "SELECT 1 FROM results WHERE cache_key = ? LIMIT 1", (cache_key,)
            )
            return cur.fetchone() is not None
        except sqlite3.Error as exc:
            raise StoreError(self.db_path, f"cache lookup failed: {exc}") from exc

    def save_result(self, cache_key: str, fp: FileFingerprint,
                    params: dict[str, object], result: DetectionResult) -> bool:
        """Insert one result and all its ranges in a single transaction.

        Returns ``True`` when a new row was written and ``False`` when the
        cache key already existed (the re-run performs no write).
        """
        if self.has_result(cache_key):
            return False
        try:
            with self._conn:
                self._conn.execute(
                    """
                    INSERT INTO results (cache_key, result_version, path, file_size,
                                         mtime_ns, params_json, sample_rate, total_frames,
                                         silence_total_seconds)
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
                    """,
                    (
                        cache_key,
                        RESULT_VERSION,
                        fp.path,
                        fp.size,
                        fp.mtime_ns,
                        json.dumps(params, sort_keys=True),
                        result.sample_rate,
                        result.total_frames,
                        result.silence_total_seconds,
                    ),
                )
                rows = [
                    (cache_key, "segment", i, seg.start_sample, seg.end_sample)
                    for i, seg in enumerate(result.segments)
                ]
                rows += [
                    (cache_key, "silence", i, seg.start_sample, seg.end_sample)
                    for i, seg in enumerate(result.silences)
                ]
                self._conn.executemany(
                    "INSERT INTO segments (cache_key, kind, ordinal, start_sample, end_sample)"
                    " VALUES (?, ?, ?, ?, ?)",
                    rows,
                )
        except sqlite3.Error as exc:
            raise StoreError(self.db_path, f"writing result failed: {exc}") from exc
        return True

    def get_segments(self, cache_key: str) -> list[Segment]:
        try:
            cur = self._conn.execute(
                "SELECT start_sample, end_sample FROM segments"
                " WHERE cache_key = ? AND kind = 'segment' ORDER BY ordinal",
                (cache_key,),
            )
            return [Segment(start, end) for start, end in cur.fetchall()]
        except sqlite3.Error as exc:
            raise StoreError(self.db_path, f"reading cached result failed: {exc}") from exc

    def close(self) -> None:
        self._conn.close()

    def __enter__(self) -> "Store":
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()
