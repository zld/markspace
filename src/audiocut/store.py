"""SQLite persistence for detection runs.

A run is keyed by ``(input_sha256, config_fingerprint)``: repeating the same
file with the same configuration is skipped, while changing either the input
or any detector parameter produces a fresh run.  Every write happens inside a
single transaction, so an interrupted transaction leaves no partial rows and
the same run can be safely retried.
"""

from __future__ import annotations

import hashlib
import json
import sqlite3
import uuid
from dataclasses import asdict
from pathlib import Path

from .detector import DetectionResult, DetectorConfig, Segment, SilenceRegion

SCHEMA = """
CREATE TABLE IF NOT EXISTS files (
    id INTEGER PRIMARY KEY,
    path TEXT NOT NULL,
    input_sha256 TEXT NOT NULL,
    sample_rate INTEGER NOT NULL,
    channels INTEGER NOT NULL,
    sample_width INTEGER NOT NULL,
    n_frames_total INTEGER NOT NULL,
    config_json TEXT NOT NULL,
    config_fingerprint TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE TABLE IF NOT EXISTS runs (
    id INTEGER PRIMARY KEY,
    run_uid TEXT NOT NULL UNIQUE,
    file_id INTEGER NOT NULL REFERENCES files(id),
    input_sha256 TEXT NOT NULL,
    config_fingerprint TEXT NOT NULL,
    noise_floor REAL NOT NULL,
    low_threshold REAL NOT NULL,
    high_threshold REAL NOT NULL,
    silence_total_seconds REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS segments (
    id INTEGER PRIMARY KEY,
    run_id INTEGER NOT NULL REFERENCES runs(id),
    seg_index INTEGER NOT NULL,
    start_sample INTEGER NOT NULL,
    end_sample INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS silences (
    id INTEGER PRIMARY KEY,
    run_id INTEGER NOT NULL REFERENCES runs(id),
    start_sample INTEGER NOT NULL,
    end_sample INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_files_input_config
    ON files(input_sha256, config_fingerprint);
"""


def hash_file(path: str | Path, chunk_size: int = 1 << 20) -> str:
    """Stream a file through SHA-256 without reading it whole."""

    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        while True:
            chunk = handle.read(chunk_size)
            if not chunk:
                break
            digest.update(chunk)
    return digest.hexdigest()


class ResultStore:
    """Thin sqlite3 wrapper; one connection per database file."""

    def __init__(self, db_path: str | Path):
        self.db_path = str(db_path)
        Path(self.db_path).parent.mkdir(parents=True, exist_ok=True)
        self.conn = sqlite3.connect(self.db_path)
        self.conn.execute("PRAGMA journal_mode=WAL")
        self.conn.execute("PRAGMA foreign_keys=ON")
        self.conn.executescript(SCHEMA)
        self.conn.commit()

    def close(self) -> None:
        self.conn.close()

    def __enter__(self) -> "ResultStore":
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        self.close()

    def find_run(
        self, input_sha256: str, config_fingerprint: str
    ) -> int | None:
        """Return the file row id for an identical input+config, else None."""

        row = self.conn.execute(
            "SELECT id FROM files WHERE input_sha256 = ? AND config_fingerprint = ?",
            (input_sha256, config_fingerprint),
        ).fetchone()
        return None if row is None else int(row[0])

    def load_result(self, file_id: int, path: str, config: DetectorConfig) -> DetectionResult:
        """Reconstruct a DetectionResult from a previously stored run."""

        file_row = self.conn.execute(
            "SELECT sample_rate, n_frames_total FROM files WHERE id = ?", (file_id,)
        ).fetchone()
        run_row = self.conn.execute(
            "SELECT noise_floor, low_threshold, high_threshold, silence_total_seconds "
            "FROM runs WHERE file_id = ?",
            (file_id,),
        ).fetchone()
        seg_rows = self.conn.execute(
            "SELECT seg_index, start_sample, end_sample FROM segments "
            "JOIN runs ON runs.id = segments.run_id WHERE runs.file_id = ? ORDER BY seg_index",
            (file_id,),
        ).fetchall()
        sil_rows = self.conn.execute(
            "SELECT start_sample, end_sample FROM silences "
            "JOIN runs ON runs.id = silences.run_id WHERE runs.file_id = ? "
            "ORDER BY start_sample",
            (file_id,),
        ).fetchall()
        return DetectionResult(
            path=path,
            sample_rate=file_row[0],
            n_frames_total=file_row[1],
            frame_len=max(1, int(round(config.frame_ms * file_row[0] / 1000.0))),
            config_fingerprint=config.fingerprint(),
            noise_floor=run_row[0],
            low_threshold=run_row[1],
            high_threshold=run_row[2],
            segments=[Segment(i, s, e) for i, s, e in seg_rows],
            silences=[SilenceRegion(s, e) for s, e in sil_rows],
            silence_total_seconds=run_row[3],
        )

    def save_run(
        self,
        result: DetectionResult,
        config: DetectorConfig,
        input_sha256: str,
        channels: int,
        sample_width: int,
        source_path: str,
        retries: int = 2,
    ) -> int:
        """Persist one result atomically; rollback + retry on failure.

        Returns the file row id.  A stale row with the same key is replaced so
        that retrying after an interrupted transaction cannot raise a
        uniqueness error.
        """

        last_error: Exception | None = None
        for _attempt in range(retries + 1):
            try:
                return self._save_once(
                    result, config, input_sha256, channels, sample_width, source_path
                )
            except sqlite3.Error as exc:
                last_error = exc
                self.conn.rollback()
        raise RuntimeError(f"failed to persist run after {retries + 1} attempts: {last_error}")

    def _save_once(
        self, result, config, input_sha256, channels, sample_width, source_path
    ) -> int:
        cur = self.conn.cursor()
        cur.execute("BEGIN IMMEDIATE")
        try:
            cur.execute("SELECT id FROM files WHERE input_sha256 = ? AND config_fingerprint = ?",
                        (input_sha256, result.config_fingerprint))
            existing = cur.fetchone()
            if existing is not None:
                old_id = existing[0]
                cur.execute("DELETE FROM segments WHERE run_id IN (SELECT id FROM runs WHERE file_id = ?)", (old_id,))
                cur.execute("DELETE FROM silences WHERE run_id IN (SELECT id FROM runs WHERE file_id = ?)", (old_id,))
                cur.execute("DELETE FROM runs WHERE file_id = ?", (old_id,))
                cur.execute("DELETE FROM files WHERE id = ?", (old_id,))

            cur.execute(
                "INSERT INTO files (path, input_sha256, sample_rate, channels, sample_width, "
                "n_frames_total, config_json, config_fingerprint) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
                (source_path, input_sha256, result.sample_rate, channels, sample_width,
                 result.n_frames_total, json.dumps(asdict(config), sort_keys=True),
                 result.config_fingerprint),
            )
            file_id = cur.lastrowid
            run_uid = uuid.uuid4().hex
            cur.execute(
                "INSERT INTO runs (run_uid, file_id, input_sha256, config_fingerprint, "
                "noise_floor, low_threshold, high_threshold, silence_total_seconds) "
                "VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
                (run_uid, file_id, input_sha256, result.config_fingerprint,
                 result.noise_floor, result.low_threshold, result.high_threshold,
                 result.silence_total_seconds),
            )
            run_id = cur.lastrowid
            cur.executemany(
                "INSERT INTO segments (run_id, seg_index, start_sample, end_sample) VALUES (?, ?, ?, ?)",
                [(run_id, s.index, s.start_sample, s.end_sample) for s in result.segments],
            )
            cur.executemany(
                "INSERT INTO silences (run_id, start_sample, end_sample) VALUES (?, ?, ?)",
                [(run_id, r.start_sample, r.end_sample) for r in result.silences],
            )
            self.conn.commit()
            return int(file_id)
        except Exception:
            self.conn.rollback()
            raise
