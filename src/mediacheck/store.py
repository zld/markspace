"""SQLite persistence for scan batches, media records and track records.

A scan is one batch. All writes for a batch happen inside a single
transaction: if anything fails, the batch is rolled back and no partial
rows survive. Re-scanning a path updates the existing media row (matched on
the absolute path) instead of duplicating it, while tracks are replaced
wholesale so stale track rows never linger.
"""

from __future__ import annotations

import json
import sqlite3
from datetime import datetime, timezone
from pathlib import Path
from typing import Iterable

SCHEMA_VERSION = 1

_SCHEMA = """
CREATE TABLE IF NOT EXISTS schema_meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS batches (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at TEXT NOT NULL,
    finished_at TEXT,
    status TEXT NOT NULL,
    root TEXT NOT NULL,
    files_ok INTEGER NOT NULL DEFAULT 0,
    files_failed INTEGER NOT NULL DEFAULT 0,
    note TEXT
);
CREATE TABLE IF NOT EXISTS media (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    batch_id INTEGER NOT NULL REFERENCES batches(id) ON DELETE CASCADE,
    path TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    container TEXT NOT NULL,
    file_size INTEGER NOT NULL,
    track_count INTEGER NOT NULL,
    duration_seconds REAL,
    codec_summary TEXT NOT NULL,
    summary_json TEXT NOT NULL,
    first_seen_batch INTEGER NOT NULL,
    last_seen_batch INTEGER NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS tracks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    track_index INTEGER NOT NULL,
    track_id INTEGER,
    kind TEXT NOT NULL,
    codec TEXT,
    timescale INTEGER,
    duration_seconds REAL,
    start_seconds REAL,
    width INTEGER,
    height INTEGER,
    sample_rate INTEGER,
    channels INTEGER,
    summary_json TEXT NOT NULL,
    UNIQUE (media_id, track_index)
);
CREATE TABLE IF NOT EXISTS failures (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    batch_id INTEGER NOT NULL REFERENCES batches(id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    code TEXT NOT NULL,
    message TEXT NOT NULL,
    offset INTEGER,
    box_path TEXT
);
CREATE INDEX IF NOT EXISTS idx_media_batch ON media (last_seen_batch);
CREATE INDEX IF NOT EXISTS idx_tracks_media ON tracks (media_id);
CREATE INDEX IF NOT EXISTS idx_failures_batch ON failures (batch_id);
"""


class StoreError(Exception):
    def __init__(self, message: str, *, path: str | None = None):
        super().__init__(message)
        self.message = message
        self.path = path

    def to_dict(self) -> dict:
        return {"path": self.path, "code": "store_error", "message": self.message}


def _now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def _normalize(path: str | Path) -> str:
    return str(Path(path).expanduser().resolve())


class MediaStore:
    def __init__(self, db_path: str | Path):
        self.db_path = Path(db_path)
        self._closed = False
        try:
            self.db_path.parent.mkdir(parents=True, exist_ok=True)
            self.conn = sqlite3.connect(str(self.db_path))
            self.conn.execute("PRAGMA foreign_keys = ON")
            self.conn.row_factory = sqlite3.Row
            with self.conn:
                self.conn.executescript(_SCHEMA)
                self.conn.execute(
                    "INSERT OR REPLACE INTO schema_meta (key, value) VALUES (?, ?)",
                    ("schema_version", str(SCHEMA_VERSION)),
                )
        except (sqlite3.Error, OSError) as exc:
            raise StoreError(f"cannot open database {self.db_path}: {exc}",
                             path=str(self.db_path)) from None

    def close(self) -> None:
        if not self._closed:
            self.conn.close()
            self._closed = True

    def __enter__(self) -> "MediaStore":
        return self

    def __exit__(self, *exc_info) -> None:
        self.close()

    def begin_batch(self, root: str | Path) -> int:
        try:
            with self.conn:
                cur = self.conn.execute(
                    "INSERT INTO batches (started_at, status, root) VALUES (?, ?, ?)",
                    (_now(), "running", _normalize(root)),
                )
            return int(cur.lastrowid)
        except sqlite3.Error as exc:
            raise StoreError(f"cannot create batch: {exc}") from None

    def commit_batch(self, batch_id: int, *, summaries: Iterable[dict],
                     failures: Iterable[dict],
                     root: str | Path | None = None) -> dict[str, int]:
        """Write all media, tracks and failures for a batch atomically.

        ``root`` disambiguates relative summary paths: they are resolved
        against the scan root, not the process working directory.
        """
        summaries = [self._normalize_summary(s, root) for s in summaries]
        failures = list(failures)
        ok_count = 0
        track_count = 0
        try:
            with self.conn:  # transaction: commits on success, rolls back on error
                for summary in summaries:
                    track_count += self._upsert_media(batch_id, summary)
                    ok_count += 1
                for failure in failures:
                    self.conn.execute(
                        "INSERT INTO failures (batch_id, path, code, message, offset, box_path)"
                        " VALUES (?, ?, ?, ?, ?, ?)",
                        (batch_id, _normalize(failure.get("path", "")),
                         failure.get("code", "unknown"),
                         failure.get("message", ""),
                         failure.get("offset"), failure.get("box_path")),
                    )
                self.conn.execute(
                    "UPDATE batches SET finished_at = ?, status = ?, files_ok = ?,"
                    " files_failed = ? WHERE id = ?",
                    (_now(), "complete", ok_count, len(failures), batch_id),
                )
        except (sqlite3.Error, OSError, KeyError, TypeError, ValueError) as exc:
            self._mark_failed(batch_id, str(exc))
            raise StoreError(f"batch {batch_id} rolled back: {exc}") from None
        return {"files_ok": ok_count, "tracks_written": track_count,
                "failures": len(failures)}

    def _mark_failed(self, batch_id: int, message: str) -> None:
        try:
            self.conn.rollback()
            with self.conn:
                self.conn.execute(
                    "UPDATE batches SET status = ?, note = ?, finished_at = ? WHERE id = ?",
                    ("rolled_back", message, _now(), batch_id),
                )
        except sqlite3.Error:
            pass

    @staticmethod
    def _normalize_summary(summary: dict, root: str | Path | None) -> dict:
        """Rewrite a summary's path/relative_path to a coherent absolute pair."""
        if root is None:
            return summary
        root_path = Path(root).expanduser().resolve()
        raw = summary.get("path")
        if raw is None:
            return summary
        candidate = Path(raw)
        if not candidate.is_absolute():
            candidate = root_path / candidate
        absolute = candidate.expanduser().resolve()
        summary = dict(summary)
        summary["path"] = str(absolute)
        try:
            summary["relative_path"] = absolute.relative_to(root_path).as_posix()
        except ValueError:
            summary.setdefault("relative_path", summary.get("name", absolute.name))
        return summary

    def _upsert_media(self, batch_id: int, summary: dict) -> int:
        path = _normalize(summary["path"])
        imported = json.dumps(summary.get("codec_summary", []),
                               ensure_ascii=False)
        now = _now()
        row = self.conn.execute("SELECT id FROM media WHERE path = ?",
                                (path,)).fetchone()
        if row is None:
            cur = self.conn.execute(
                "INSERT INTO media (batch_id, path, name, container, file_size,"
                " track_count, duration_seconds, codec_summary, summary_json,"
                " first_seen_batch, last_seen_batch, updated_at)"
                " VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
                (batch_id, path, summary.get("name", Path(path).name),
                 summary.get("container", "unknown"),
                 int(summary.get("file_size", 0)),
                 int(summary.get("track_count", 0)),
                 summary.get("duration_seconds"), imported,
                 json.dumps(summary, ensure_ascii=False),
                 batch_id, batch_id, now),
            )
            media_id = int(cur.lastrowid)
        else:
            media_id = int(row["id"])
            self.conn.execute(
                "UPDATE media SET batch_id = ?, name = ?, container = ?,"
                " file_size = ?, track_count = ?, duration_seconds = ?,"
                " codec_summary = ?, summary_json = ?, last_seen_batch = ?,"
                " updated_at = ? WHERE id = ?",
                (batch_id, summary.get("name", Path(path).name),
                 summary.get("container", "unknown"),
                 int(summary.get("file_size", 0)),
                 int(summary.get("track_count", 0)),
                 summary.get("duration_seconds"), imported,
                 json.dumps(summary, ensure_ascii=False),
                 batch_id, now, media_id),
            )
            self.conn.execute("DELETE FROM tracks WHERE media_id = ?", (media_id,))
        written = 0
        for track in summary.get("tracks", []):
            self.conn.execute(
                "INSERT INTO tracks (media_id, track_index, track_id, kind, codec,"
                " timescale, duration_seconds, start_seconds, width, height,"
                " sample_rate, channels, summary_json)"
                " VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
                (media_id, int(track.get("index", written)),
                 track.get("track_id"),
                 track.get("kind", "other"), track.get("codec"),
                 track.get("timescale"), track.get("duration_seconds"),
                 track.get("start_seconds"), track.get("width"),
                 track.get("height"), track.get("sample_rate"),
                 track.get("channels"),
                 json.dumps(track, ensure_ascii=False)),
            )
            written += 1
        return written

    # ------------------------------------------------------------------
    # Read side used by report / compare
    # ------------------------------------------------------------------

    def latest_batch(self) -> dict | None:
        row = self.conn.execute(
            "SELECT * FROM batches WHERE status = 'complete' ORDER BY id DESC LIMIT 1"
        ).fetchone()
        return dict(row) if row else None

    def batch_failures(self, batch_id: int) -> list[dict]:
        rows = self.conn.execute(
            "SELECT * FROM failures WHERE batch_id = ? ORDER BY path",
            (batch_id,)).fetchall()
        return [dict(r) for r in rows]

    def batch_summaries(self, batch_id: int) -> list[dict]:
        rows = self.conn.execute(
            "SELECT summary_json FROM media WHERE last_seen_batch = ? ORDER BY path",
            (batch_id,)).fetchall()
        return [json.loads(r["summary_json"]) for r in rows]

    def media_count(self) -> int:
        return int(self.conn.execute("SELECT COUNT(*) FROM media").fetchone()[0])

    def track_count(self) -> int:
        return int(self.conn.execute("SELECT COUNT(*) FROM tracks").fetchone()[0])


def batch_manifest(store: MediaStore, batch_id: int | None = None) -> dict:
    """Build a manifest-shaped document from persisted batch records.

    This lets compare/report consume database history through exactly the
    same structure that pack writes to JSON.
    """
    from . import MANIFEST_VERSION

    batch = (store.latest_batch() if batch_id is None
             else _get_batch(store, batch_id))
    if batch is None:
        raise StoreError("no completed scan batch found")
    summaries = store.batch_summaries(int(batch["id"]))
    # Comparisons key media by manifest-relative path. Recompute it from the
    # batch root so database-backed and pack-based manifests agree on nested
    # directory trees.
    root = Path(batch["root"])
    for summary in summaries:
        try:
            summary["relative_path"] = Path(summary["path"]).relative_to(
                root).as_posix()
        except ValueError:
            summary["relative_path"] = summary.get("name") or summary["path"]
    failures = store.batch_failures(int(batch["id"]))
    return {
        "manifest_version": MANIFEST_VERSION,
        "kind": "mediacheck.manifest",
        "source": "database",
        "label": f"db:{store.db_path}@batch{batch['id']}",
        "root": batch["root"],
        "batch": {
            "id": batch["id"],
            "started_at": batch["started_at"],
            "finished_at": batch["finished_at"],
            "status": batch["status"],
            "files_ok": batch["files_ok"],
            "files_failed": batch["files_failed"],
        },
        "files_scanned": int(batch["files_ok"]) + int(batch["files_failed"]),
        "files_ok": int(batch["files_ok"]),
        "files_failed": int(batch["files_failed"]),
        "tracks_total": sum(s.get("track_count", 0) for s in summaries),
        "media": summaries,
        "failures": failures,
    }


def _get_batch(store: MediaStore, batch_id: int) -> dict | None:
    row = store.conn.execute("SELECT * FROM batches WHERE id = ?",
                             (batch_id,)).fetchone()
    return dict(row) if row else None
