"""SQLite persistence for tasks, detection parameters, segments and errors.

Every file result (task row, result row and all child segment/silence/error
rows) is written inside a single ``BEGIN IMMEDIATE`` transaction, so an
interrupted process can never leave a half-written result. Re-running the
same input fingerprint with identical parameters reuses the stored result;
changed parameters produce a brand new result row.
"""
from __future__ import annotations

import hashlib
import sqlite3
from pathlib import Path

from . import SCHEMA_VERSION

SCHEMA = """
CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE tasks (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    path          TEXT NOT NULL,
    sha256        TEXT NOT NULL,
    byte_size     INTEGER NOT NULL,
    sample_rate   INTEGER NOT NULL,
    n_samples     INTEGER NOT NULL,
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE results (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id                INTEGER NOT NULL REFERENCES tasks(id),
    param_fingerprint      TEXT NOT NULL,
    params_json            TEXT NOT NULL,
    status                 TEXT NOT NULL CHECK (status IN ('ok', 'error')),
    close_threshold_db     REAL,
    open_threshold_db      REAL,
    noise_floor_db         REAL,
    silence_total_samples  INTEGER,
    error_message          TEXT,
    created_at             TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE segments (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    result_id    INTEGER NOT NULL REFERENCES results(id),
    kind         TEXT NOT NULL CHECK (kind IN ('merged', 'raw')),
    ord          INTEGER NOT NULL,
    start_sample INTEGER NOT NULL,
    end_sample   INTEGER NOT NULL
);
CREATE TABLE silence_zones (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    result_id    INTEGER NOT NULL REFERENCES results(id),
    ord          INTEGER NOT NULL,
    start_sample INTEGER NOT NULL,
    end_sample   INTEGER NOT NULL
);
"""


class StorageError(RuntimeError):
    """Database is missing, corrupt or from an incompatible schema version."""


def file_fingerprint(path: str | Path) -> tuple[str, int]:
    h = hashlib.sha256()
    p = Path(path)
    size = 0
    with p.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
            size += len(chunk)
    return h.hexdigest(), size


class Storage:
    def __init__(self, db_path: str | Path):
        self.db_path = Path(db_path)
        parent = self.db_path.parent
        if str(parent) not in ("", ".") and not parent.exists():
            parent.mkdir(parents=True, exist_ok=True)
        try:
            self.conn = sqlite3.connect(str(self.db_path))
            self.conn.row_factory = sqlite3.Row
            self.conn.execute("PRAGMA foreign_keys = ON")
            self._init_schema()
        except sqlite3.DatabaseError as exc:
            raise StorageError(
                f"database {self.db_path} is corrupt or not a SQLite database: {exc}. "
                "Move or delete the file and re-run to create a fresh database."
            ) from exc

    def _init_schema(self) -> None:
        try:
            tables = {
                r[0]
                for r in self.conn.execute(
                    "SELECT name FROM sqlite_master WHERE type = 'table'"
                ).fetchall()
            }
        except sqlite3.DatabaseError as exc:
            raise StorageError(
                f"database {self.db_path} is corrupt or not a SQLite database: {exc}. "
                "Move or delete the file and re-run to create a fresh database."
            ) from exc
        if not tables:
            # Brand-new empty database: initialise it.
            self.conn.executescript(SCHEMA)
            self.conn.execute(
                "INSERT INTO meta (key, value) VALUES ('schema_version', ?)",
                (str(SCHEMA_VERSION),),
            )
            self.conn.commit()
            return
        if "meta" not in tables:
            raise StorageError(
                f"database {self.db_path} is missing required tables and may be from "
                "an incompatible release; use a new --db path instead of overwriting it"
            )
        try:
            row = self.conn.execute(
                "SELECT value FROM meta WHERE key = 'schema_version'"
            ).fetchone()
        except sqlite3.DatabaseError as exc:
            raise StorageError(
                f"database {self.db_path} is corrupt or not a SQLite database: {exc}. "
                "Move or delete the file and re-run to create a fresh database."
            ) from exc
        try:
            version = int(row["value"])
        except (TypeError, ValueError) as exc:
            raise StorageError(
                f"database {self.db_path} has an unreadable schema version"
            ) from exc
        if version != SCHEMA_VERSION:
            raise StorageError(
                f"database schema version {version} is incompatible with this "
                f"release (expected {SCHEMA_VERSION}); use a new --db path or "
                "migrate the existing database"
            )

    def close(self) -> None:
        self.conn.close()

    def __enter__(self) -> "Storage":
        return self

    def __exit__(self, *exc) -> None:
        self.close()

    def _get_or_create_task(
        self, path: str, sha256: str, byte_size: int, sample_rate: int, n_samples: int
    ) -> int:
        row = self.conn.execute(
            "SELECT id FROM tasks WHERE sha256 = ? ORDER BY id LIMIT 1", (sha256,)
        ).fetchone()
        if row is not None:
            return int(row["id"])
        cur = self.conn.execute(
            "INSERT INTO tasks (path, sha256, byte_size, sample_rate, n_samples) "
            "VALUES (?, ?, ?, ?, ?)",
            (path, sha256, byte_size, sample_rate, n_samples),
        )
        return int(cur.lastrowid)

    def lookup_result(self, sha256: str, param_fingerprint: str) -> dict | None:
        """Return an existing successful result for this exact input+params, else None."""
        row = self.conn.execute(
            """
            SELECT r.* FROM results r
            JOIN tasks t ON t.id = r.task_id
            WHERE t.sha256 = ? AND r.param_fingerprint = ? AND r.status = 'ok'
            ORDER BY r.id DESC LIMIT 1
            """,
            (sha256, param_fingerprint),
        ).fetchone()
        if row is None:
            return None
        return self._load_result(int(row["id"]), dict(row))

    def _load_result(self, result_id: int, base: dict) -> dict:
        def regions(table: str) -> list[dict]:
            rows = self.conn.execute(
                f"SELECT start_sample, end_sample FROM {table} "
                "WHERE result_id = ? ORDER BY ord",
                (result_id,),
            ).fetchall()
            return [
                {"start_sample": int(r["start_sample"]), "end_sample": int(r["end_sample"])}
                for r in rows
            ]
        base["segments"] = [
            r
            for r in self.conn.execute(
                "SELECT start_sample, end_sample FROM segments "
                "WHERE result_id = ? AND kind = 'merged' ORDER BY ord",
                (result_id,),
            ).fetchall()
        ]
        base["segments"] = [
            {"start_sample": int(r["start_sample"]), "end_sample": int(r["end_sample"])}
            for r in base["segments"]
        ]
        base["raw_segments"] = [
            {"start_sample": int(r["start_sample"]), "end_sample": int(r["end_sample"])}
            for r in self.conn.execute(
                "SELECT start_sample, end_sample FROM segments "
                "WHERE result_id = ? AND kind = 'raw' ORDER BY ord",
                (result_id,),
            ).fetchall()
        ]
        base["silence_regions"] = regions("silence_zones")
        return base

    def save_result(
        self,
        *,
        path: str,
        sha256: str,
        byte_size: int,
        sample_rate: int,
        n_samples: int,
        params_json: str,
        param_fingerprint: str,
        detection: dict | None,
        error_message: str | None,
    ) -> int:
        """Atomically insert one full file result; returns the new result id."""
        status = "ok" if detection is not None else "error"
        try:
            self.conn.execute("BEGIN IMMEDIATE")
            task_id = self._get_or_create_task(
                path, sha256, byte_size, sample_rate, n_samples
            )
            cur = self.conn.execute(
                "INSERT INTO results (task_id, param_fingerprint, params_json, status, "
                "close_threshold_db, open_threshold_db, noise_floor_db, "
                "silence_total_samples, error_message) "
                "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
                (
                    task_id,
                    param_fingerprint,
                    params_json,
                    status,
                    None if detection is None else detection["close_threshold_db"],
                    None if detection is None else detection["open_threshold_db"],
                    None if detection is None else detection["noise_floor_db"],
                    None if detection is None else detection["silence_total_samples"],
                    error_message,
                ),
            )
            result_id = int(cur.lastrowid)
            if detection is not None:
                for kind, key in (("merged", "segments"), ("raw", "raw_segments")):
                    for i, region in enumerate(detection[key]):
                        self.conn.execute(
                            "INSERT INTO segments (result_id, kind, ord, start_sample, end_sample) "
                            "VALUES (?, ?, ?, ?, ?)",
                            (result_id, kind, i, region["start_sample"], region["end_sample"]),
                        )
                for i, region in enumerate(detection["silence_regions"]):
                    self.conn.execute(
                        "INSERT INTO silence_zones (result_id, ord, start_sample, end_sample) "
                        "VALUES (?, ?, ?, ?)",
                        (result_id, i, region["start_sample"], region["end_sample"]),
                    )
            self.conn.commit()
        except Exception:
            self.conn.rollback()
            raise
        return result_id

    def list_results(self, task: str | None = None) -> list[dict]:
        sql = (
            "SELECT r.id AS result_id, r.status, r.created_at, r.error_message, "
            "r.silence_total_samples, t.id AS task_id, t.path, t.sample_rate, "
            "t.n_samples, r.param_fingerprint "
            "FROM results r JOIN tasks t ON t.id = r.task_id"
        )
        params: tuple = ()
        if task:
            sql += " WHERE t.id = ? OR t.path = ?"
            params = (task, task)
        sql += " ORDER BY t.path, r.id"
        rows = [dict(r) for r in self.conn.execute(sql, params).fetchall()]
        for row in rows:
            count = self.conn.execute(
                "SELECT COUNT(*) AS c FROM segments WHERE result_id = ? AND kind = 'merged'",
                (row["result_id"],),
            ).fetchone()["c"]
            row["segment_count"] = int(count)
        return rows

    def get_result(self, result_id: int) -> dict | None:
        row = self.conn.execute(
            "SELECT * FROM results WHERE id = ?", (result_id,)
        ).fetchone()
        if row is None:
            return None
        return self._load_result(result_id, dict(row))
