"""SQLite persistence for detection runs, segments, and silence ranges.

Each run is written in one transaction.  On any failure the transaction is
rolled back, so previously committed runs survive intact.
"""

from __future__ import annotations

import sqlite3
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

from .config import DetectionConfig
from .segments import SegmentPlan

SCHEMA_VERSION = 1

_SCHEMA = """
CREATE TABLE IF NOT EXISTS runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    batch_id TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    config_digest TEXT NOT NULL,
    config_json TEXT NOT NULL,
    input_path TEXT NOT NULL,
    relative_path TEXT NOT NULL,
    sample_rate INTEGER NOT NULL,
    total_samples INTEGER NOT NULL,
    silence_samples INTEGER NOT NULL,
    segment_count INTEGER NOT NULL,
    filtered_short INTEGER NOT NULL DEFAULT 0,
    filtered_silent INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_runs_lookup
    ON runs(fingerprint, config_digest);
CREATE TABLE IF NOT EXISTS segments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    idx INTEGER NOT NULL,
    start_sample INTEGER NOT NULL,
    end_sample INTEGER NOT NULL,
    fade_in_samples INTEGER NOT NULL,
    fade_out_samples INTEGER NOT NULL,
    peak_dbfs REAL NOT NULL,
    rms_dbfs REAL NOT NULL,
    UNIQUE(run_id, idx)
);
CREATE TABLE IF NOT EXISTS silences (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    start_sample INTEGER NOT NULL,
    end_sample INTEGER NOT NULL,
    UNIQUE(run_id, start_sample)
);
CREATE TABLE IF NOT EXISTS meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
"""


class StoreError(Exception):
    """Raised for database open/read/write problems."""


@dataclass(frozen=True)
class RunRecord:
    run_id: int
    batch_id: str
    fingerprint: str
    input_path: str
    relative_path: str
    sample_rate: int
    total_samples: int
    silence_samples: int
    segment_count: int
    created_at: str


@dataclass(frozen=True)
class StoredSegment:
    idx: int
    start_sample: int
    end_sample: int
    fade_in_samples: int
    fade_out_samples: int
    peak_dbfs: float
    rms_dbfs: float


def _record(row: sqlite3.Row) -> RunRecord:
    return RunRecord(
        run_id=int(row["id"]),
        batch_id=str(row["batch_id"]),
        fingerprint=str(row["fingerprint"]),
        input_path=str(row["input_path"]),
        relative_path=str(row["relative_path"]),
        sample_rate=int(row["sample_rate"]),
        total_samples=int(row["total_samples"]),
        silence_samples=int(row["silence_samples"]),
        segment_count=int(row["segment_count"]),
        created_at=str(row["created_at"]),
    )


class Store:
    def __init__(self, path: Path) -> None:
        self.path = Path(path)
        self._conn: sqlite3.Connection | None = None

    def open(self) -> "Store":
        try:
            self.path.parent.mkdir(parents=True, exist_ok=True)
            self._conn = sqlite3.connect(str(self.path))
            self._conn.row_factory = sqlite3.Row
            self._conn.execute("PRAGMA foreign_keys = ON")
            self._conn.executescript(_SCHEMA)
            self._conn.execute(
                "INSERT OR IGNORE INTO meta(key, value) VALUES('schema_version', ?)",
                (str(SCHEMA_VERSION),),
            )
            self._conn.commit()
        except (sqlite3.Error, OSError) as exc:
            raise StoreError(f"cannot open database {self.path}: {exc}") from exc
        return self

    def close(self) -> None:
        if self._conn is not None:
            self._conn.close()
            self._conn = None

    def __enter__(self) -> "Store":
        return self.open()

    def __exit__(self, exc_type, exc, tb) -> None:
        self.close()

    @property
    def conn(self) -> sqlite3.Connection:
        if self._conn is None:
            raise StoreError("database is not open")
        return self._conn

    def find_run(self, fingerprint: str, config: DetectionConfig) -> RunRecord | None:
        try:
            row = self.conn.execute(
                "SELECT * FROM runs WHERE fingerprint = ? AND config_digest = ? "
                "ORDER BY id DESC LIMIT 1",
                (fingerprint, config.digest()),
            ).fetchone()
        except sqlite3.Error as exc:
            raise StoreError(f"database query failed: {exc}") from exc
        return _record(row) if row is not None else None

    def save_run(
        self,
        *,
        batch_id: str,
        fingerprint: str,
        config: DetectionConfig,
        input_path: str,
        relative_path: str,
        plan: SegmentPlan,
        created_at: str | None = None,
    ) -> RunRecord:
        stamp = created_at or datetime.now(timezone.utc).isoformat(timespec="seconds")
        self._validate_plan(plan)
        conn = self.conn
        try:
            with conn:
                cursor = conn.execute(
                    "INSERT INTO runs(batch_id, fingerprint, config_digest, "
                    "config_json, input_path, relative_path, sample_rate, "
                    "total_samples, silence_samples, segment_count, "
                    "filtered_short, filtered_silent, created_at) "
                    "VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)",
                    (
                        batch_id,
                        fingerprint,
                        config.digest(),
                        config.canonical_json(),
                        input_path,
                        relative_path,
                        plan.sample_rate,
                        plan.total_samples,
                        plan.silence_samples,
                        len(plan.segments),
                        plan.filtered_short,
                        plan.filtered_silent,
                        stamp,
                    ),
                )
                run_id = int(cursor.lastrowid)
                conn.executemany(
                    "INSERT INTO segments(run_id, idx, start_sample, end_sample, "
                    "fade_in_samples, fade_out_samples, peak_dbfs, rms_dbfs) "
                    "VALUES(?,?,?,?,?,?,?,?)",
                    [
                        (
                            run_id,
                            item.index,
                            item.start,
                            item.end,
                            item.fade_in,
                            item.fade_out,
                            item.peak_dbfs,
                            item.rms_dbfs,
                        )
                        for item in plan.segments
                    ],
                )
                conn.executemany(
                    "INSERT INTO silences(run_id, start_sample, end_sample) "
                    "VALUES(?,?,?)",
                    [(run_id, start, end) for start, end in plan.silence],
                )
        except sqlite3.Error as exc:
            raise StoreError(f"failed to save run: {exc}") from exc
        return RunRecord(
            run_id=run_id,
            batch_id=batch_id,
            fingerprint=fingerprint,
            input_path=input_path,
            relative_path=relative_path,
            sample_rate=plan.sample_rate,
            total_samples=plan.total_samples,
            silence_samples=plan.silence_samples,
            segment_count=len(plan.segments),
            created_at=stamp,
        )

    @staticmethod
    def _validate_plan(plan: SegmentPlan) -> None:
        total = plan.total_samples
        if total < 0 or plan.sample_rate <= 0:
            raise StoreError("invalid plan dimensions")
        ranges = [(item.start, item.end) for item in plan.segments]
        ranges.extend(plan.silence)
        for start, end in ranges:
            if not (0 <= start <= end <= total):
                raise StoreError(f"invalid range [{start}, {end}) for {total} samples")

    def load_segments(self, run_id: int) -> list[StoredSegment]:
        try:
            rows = self.conn.execute(
                "SELECT idx, start_sample, end_sample, fade_in_samples, "
                "fade_out_samples, peak_dbfs, rms_dbfs FROM segments "
                "WHERE run_id = ? ORDER BY idx",
                (run_id,),
            ).fetchall()
        except sqlite3.Error as exc:
            raise StoreError(f"database query failed: {exc}") from exc
        return [
            StoredSegment(
                idx=int(row["idx"]),
                start_sample=int(row["start_sample"]),
                end_sample=int(row["end_sample"]),
                fade_in_samples=int(row["fade_in_samples"]),
                fade_out_samples=int(row["fade_out_samples"]),
                peak_dbfs=float(row["peak_dbfs"]),
                rms_dbfs=float(row["rms_dbfs"]),
            )
            for row in rows
        ]

    def load_silences(self, run_id: int) -> list[tuple[int, int]]:
        try:
            rows = self.conn.execute(
                "SELECT start_sample, end_sample FROM silences "
                "WHERE run_id = ? ORDER BY start_sample",
                (run_id,),
            ).fetchall()
        except sqlite3.Error as exc:
            raise StoreError(f"database query failed: {exc}") from exc
        return [(int(r["start_sample"]), int(r["end_sample"])) for r in rows]

    def load_config_json(self, run_id: int) -> str:
        try:
            row = self.conn.execute(
                "SELECT config_json FROM runs WHERE id = ?", (run_id,)
            ).fetchone()
        except sqlite3.Error as exc:
            raise StoreError(f"database query failed: {exc}") from exc
        if row is None:
            raise StoreError(f"run {run_id} does not exist")
        return str(row["config_json"])

    def run_summary(self, batch_id: str) -> list[RunRecord]:
        try:
            rows = self.conn.execute(
                "SELECT * FROM runs WHERE batch_id = ? ORDER BY relative_path, id",
                (batch_id,),
            ).fetchall()
        except sqlite3.Error as exc:
            raise StoreError(f"database query failed: {exc}") from exc
        return [_record(row) for row in rows]
