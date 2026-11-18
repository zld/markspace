"""SQLite persistence for scan snapshots and compare runs.

A scan is keyed by ``(sha256, tool_version)``.  Re-importing an unchanged
file reuses the stored snapshot instead of inserting a duplicate.  Each
compare run and all of its files/tracks are written inside one transaction,
so a failed write never leaves a partial scan behind.
"""

from __future__ import annotations

import json
import sqlite3
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from . import __version__
from .compare import CompareReport, FileReport
from .model import SniffResult, Track
from .schema import _SCHEMA, SCHEMA_VERSION

class StoreError(Exception):
    """Raised for local storage failures (surfaced as exit code 3)."""


@dataclass(slots=True)
class StoredScan:
    scan_id: int
    sha256: str
    reused: bool


class Store:
    def __init__(self, path: str | Path):
        self.path = str(path)
        try:
            self.conn = sqlite3.connect(self.path)
            self.conn.execute("PRAGMA foreign_keys = ON")
            self.conn.executescript(_SCHEMA)
            self.conn.execute(
                "INSERT OR REPLACE INTO meta(key, value) VALUES('schema_version', ?)",
                (str(SCHEMA_VERSION),),
            )
            self.conn.commit()
        except sqlite3.Error as exc:
            raise StoreError(f"cannot open database {self.path}: {exc}") from exc

    def close(self) -> None:
        self.conn.close()

    def __enter__(self) -> "Store":
        return self

    def __exit__(self, *exc_info) -> None:
        self.close()

    # -- scans ---------------------------------------------------------
    def record_scan(
        self, sha256: str, result: SniffResult, tool_version: str = __version__
    ) -> StoredScan:
        try:
            with self.conn:
                row = self.conn.execute(
                    "SELECT id FROM scans WHERE sha256 = ? AND tool_version = ?",
                    (sha256, tool_version),
                ).fetchone()
                if row is not None:
                    return StoredScan(scan_id=row[0], sha256=sha256, reused=True)
                cursor = self.conn.execute(
                    """INSERT INTO scans
                       (sha256, tool_version, container, size,
                        declared_duration_ms, truncated, error, metadata_json)
                       VALUES (?, ?, ?, ?, ?, ?, ?, ?)""",
                    (
                        sha256,
                        tool_version,
                        result.container,
                        result.size,
                        result.declared_duration_ms,
                        1 if result.truncated else 0,
                        result.error,
                        json.dumps(result.metadata, ensure_ascii=False, sort_keys=True),
                    ),
                )
                scan_id = int(cursor.lastrowid)
                for track in result.tracks:
                    self._insert_track(scan_id, track)
                return StoredScan(scan_id=scan_id, sha256=sha256, reused=False)
        except sqlite3.Error as exc:
            raise StoreError(f"cannot record scan: {exc}") from exc

    def _insert_track(self, scan_id: int, track: Track) -> None:
        self.conn.execute(
            """INSERT INTO scan_tracks
               (scan_id, stable_index, kind, language, codec, duration_ms,
                sample_rate, channels, bit_depth, block_align, extra_json)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)""",
            (
                scan_id,
                track.stable_index,
                track.kind,
                track.language,
                track.codec,
                track.duration_ms,
                track.sample_rate,
                track.channels,
                track.bit_depth,
                track.block_align,
                json.dumps(track.extra, ensure_ascii=False, sort_keys=True),
            ),
        )

    def load_scan(self, scan_id: int) -> SniffResult | None:
        try:
            row = self.conn.execute(
                """SELECT container, size, declared_duration_ms, truncated,
                          error, metadata_json
                   FROM scans WHERE id = ?""",
                (scan_id,),
            ).fetchone()
            if row is None:
                return None
            tracks = [
                Track(
                    stable_index=t[0],
                    kind=t[1],
                    language=t[2],
                    codec=t[3],
                    duration_ms=t[4],
                    sample_rate=t[5],
                    channels=t[6],
                    bit_depth=t[7],
                    block_align=t[8],
                    extra=json.loads(t[9]),
                )
                for t in self.conn.execute(
                    """SELECT stable_index, kind, language, codec, duration_ms,
                              sample_rate, channels, bit_depth, block_align,
                              extra_json
                       FROM scan_tracks WHERE scan_id = ?
                       ORDER BY id""",
                    (scan_id,),
                )
            ]
        except (sqlite3.Error, json.JSONDecodeError) as exc:
            raise StoreError(f"cannot load scan {scan_id}: {exc}") from exc
        return SniffResult(
            container=row[0],
            size=row[1],
            tracks=tracks,
            declared_duration_ms=row[2],
            error=row[4],
            truncated=bool(row[3]),
            metadata=json.loads(row[5]),
        )

    def find_scan(self, sha256: str, tool_version: str = __version__) -> int | None:
        row = self.conn.execute(
            "SELECT id FROM scans WHERE sha256 = ? AND tool_version = ?",
            (sha256, tool_version),
        ).fetchone()
        return int(row[0]) if row else None

    # -- compare runs --------------------------------------------------
    def record_compare(self, report: CompareReport) -> int:
        try:
            with self.conn:
                cursor = self.conn.execute(
                    """INSERT INTO compare_runs
                       (duration_tolerance_ms, files_scanned, tracks_expected,
                        tracks_actual, mismatch_count, error_count)
                       VALUES (?, ?, ?, ?, ?, ?)""",
                    (
                        report.duration_tolerance_ms,
                        report.files_scanned,
                        report.tracks_expected,
                        report.tracks_actual,
                        len(report.mismatches),
                        len(report.errors),
                    ),
                )
                run_id = int(cursor.lastrowid)
                for file_report in report.files:
                    self._insert_compare_file(run_id, file_report)
                return run_id
        except sqlite3.Error as exc:
            raise StoreError(f"cannot record compare run: {exc}") from exc

    def _insert_compare_file(self, run_id: int, file_report: FileReport) -> None:
        expected_id = file_report.expected_scan_id
        actual_id = file_report.actual_scan_id
        cursor = self.conn.execute(
            """INSERT INTO compare_files
               (run_id, relative_path, expected_scan_id, actual_scan_id,
                mismatch_count)
               VALUES (?, ?, ?, ?, ?)""",
            (
                run_id,
                file_report.path,
                expected_id,
                actual_id,
                len(file_report.mismatches),
            ),
        )
        file_id = int(cursor.lastrowid)
        for mismatch in file_report.mismatches:
            self.conn.execute(
                """INSERT INTO compare_mismatches
                   (file_id, code, detail, expected_json, actual_json)
                   VALUES (?, ?, ?, ?, ?)""",
                (
                    file_id,
                    mismatch.code,
                    mismatch.detail,
                    _encode(mismatch.expected),
                    _encode(mismatch.actual),
                ),
            )


def _encode(value: Any) -> str | None:
    if value is None:
        return None
    return json.dumps(value, ensure_ascii=False, sort_keys=True)

