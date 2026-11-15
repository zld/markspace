"""SQLite persistence: task identity, results and run status.

Task identity is (path, content_hash, params_hash). Re-detection of the
same task reuses committed rows; a forced rerun replaces old rows inside
a single transaction so a failure never leaves half-written results.
"""
from __future__ import annotations

import json
import sqlite3
from datetime import datetime, timezone

SCHEMA = """
CREATE TABLE IF NOT EXISTS runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    params_json TEXT NOT NULL,
    params_hash TEXT NOT NULL,
    created_at TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'running'
);
CREATE TABLE IF NOT EXISTS files (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL REFERENCES runs(id),
    path TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    params_hash TEXT NOT NULL,
    status TEXT NOT NULL,
    error TEXT,
    sample_rate INTEGER, channels INTEGER, bit_depth INTEGER, n_samples INTEGER,
    silence_total_ms REAL,
    UNIQUE(path, content_hash, params_hash)
);
CREATE TABLE IF NOT EXISTS clips (
    file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    idx INTEGER NOT NULL,
    start_sample INTEGER NOT NULL,
    end_sample INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS silences (
    file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    idx INTEGER NOT NULL,
    start_sample INTEGER NOT NULL,
    end_sample INTEGER NOT NULL
);
"""


class StoreError(Exception):
    """Global storage failure (exit code 2)."""


class Store:
    def __init__(self, db_path: str):
        try:
            self.conn = sqlite3.connect(db_path)
            self.conn.row_factory = sqlite3.Row
            self.conn.execute("PRAGMA foreign_keys = ON")
            with self.conn:
                self.conn.executescript(SCHEMA)
        except sqlite3.DatabaseError as exc:
            raise StoreError(f"cannot open database {db_path}: {exc}") from exc

    def close(self) -> None:
        self.conn.close()

    def begin_run(self, params) -> int:
        now = datetime.now(timezone.utc).isoformat()
        with self.conn:
            cur = self.conn.execute(
                "INSERT INTO runs(params_json, params_hash, created_at, status)"
                " VALUES (?,?,?,'running')",
                (json.dumps(params.as_dict(), sort_keys=True),
                 params.fingerprint(), now))
            return cur.lastrowid

    def finish_run(self, run_id: int, status: str) -> None:
        with self.conn:
            self.conn.execute("UPDATE runs SET status=? WHERE id=?",
                              (status, run_id))

    def _find(self, path: str, content_hash: str, params_hash: str, status=None):
        sql = ("SELECT id, status FROM files"
               " WHERE path=? AND content_hash=? AND params_hash=?")
        args = (path, content_hash, params_hash)
        if status:
            sql += " AND status=?"
            args += (status,)
        return self.conn.execute(sql, args).fetchone()

    def lookup_ok(self, path: str, content_hash: str, params_hash: str):
        row = self._find(path, content_hash, params_hash, status="ok")
        return row["id"] if row else None

    def _delete_file(self, file_id: int) -> None:
        self.conn.execute("DELETE FROM clips WHERE file_id=?", (file_id,))
        self.conn.execute("DELETE FROM silences WHERE file_id=?", (file_id,))
        self.conn.execute("DELETE FROM files WHERE id=?", (file_id,))

    def replace_result(self, run_id, path, content_hash, params, info, result):
        """Atomically replace any previous result for this task key."""
        try:
            with self.conn:
                old = self._find(path, content_hash, params.fingerprint())
                if old:
                    self._delete_file(old["id"])
                cur = self.conn.execute(
                    "INSERT INTO files(run_id,path,content_hash,params_hash,status,"
                    "sample_rate,channels,bit_depth,n_samples,silence_total_ms)"
                    " VALUES (?,?,?,?,'ok',?,?,?,?,?)",
                    (run_id, path, content_hash, params.fingerprint(),
                     info.sample_rate, info.channels, info.bit_depth,
                     info.n_samples,
                     result.silence_total_samples * 1000.0 / info.sample_rate))
                fid = cur.lastrowid
                self.conn.executemany(
                    "INSERT INTO clips(file_id,idx,start_sample,end_sample)"
                    " VALUES (?,?,?,?)",
                    [(fid, i, c.start_sample, c.end_sample)
                     for i, c in enumerate(result.clips)])
                self.conn.executemany(
                    "INSERT INTO silences(file_id,idx,start_sample,end_sample)"
                    " VALUES (?,?,?,?)",
                    [(fid, i, s.start_sample, s.end_sample)
                     for i, s in enumerate(result.silences)])
                return fid
        except sqlite3.DatabaseError as exc:
            raise StoreError(f"failed to store result for {path}: {exc}") from exc

    def record_failure(self, run_id, path, content_hash, params_hash, error):
        try:
            with self.conn:
                old = self._find(path, content_hash, params_hash)
                if old:
                    self._delete_file(old["id"])
                self.conn.execute(
                    "INSERT INTO files(run_id,path,content_hash,params_hash,"
                    "status,error) VALUES (?,?,?,?,'failed',?)",
                    (run_id, path, content_hash, params_hash, error))
        except sqlite3.DatabaseError as exc:
            raise StoreError(f"failed to record failure for {path}: {exc}") from exc

    def clips_of(self, file_id: int):
        return self.conn.execute(
            "SELECT * FROM clips WHERE file_id=? ORDER BY idx", (file_id,)).fetchall()

    def silences_of(self, file_id: int):
        return self.conn.execute(
            "SELECT * FROM silences WHERE file_id=? ORDER BY idx", (file_id,)).fetchall()

    def file_record(self, file_id: int):
        row = self.conn.execute("SELECT * FROM files WHERE id=?",
                                (file_id,)).fetchone()
        if row is None:
            return None
        return {"row": row, "clips": self.clips_of(file_id),
                "silences": self.silences_of(file_id),
                "silence_total_ms": row["silence_total_ms"] or 0.0}

    def ok_files(self):
        """Latest committed ok record per path, stably ordered."""
        return self.conn.execute(
            "SELECT f.*, r.params_json AS params_json FROM files f"
            " JOIN runs r ON r.id = f.run_id"
            " WHERE f.status='ok' AND f.id IN"
            "   (SELECT MAX(id) FROM files WHERE status='ok' GROUP BY path)"
            " ORDER BY f.path").fetchall()

    def query_files(self, path=None):
        sql = ("SELECT f.*, (SELECT COUNT(*) FROM clips c WHERE c.file_id=f.id)"
               " AS clip_count FROM files f")
        args = ()
        if path:
            sql += " WHERE f.path=?"
            args = (path,)
        sql += " ORDER BY f.path, f.id"
        return self.conn.execute(sql, args).fetchall()
