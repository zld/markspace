"""SQLite persistence for batch inputs, parse results, and issues."""

from __future__ import annotations

import hashlib
import json
import sqlite3
from contextlib import closing
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from .models import Container, Issue, Track
from .rules import BatchReport, RULES_VERSION

SCHEMA = """
PRAGMA foreign_keys = ON;
CREATE TABLE IF NOT EXISTS batches (
    batch_id      TEXT PRIMARY KEY,
    created_at    TEXT NOT NULL,
    rules_version TEXT NOT NULL,
    options       TEXT NOT NULL,
    inputs        TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS files (
    batch_id     TEXT NOT NULL REFERENCES batches(batch_id) ON DELETE CASCADE,
    path         TEXT NOT NULL,
    sha256       TEXT NOT NULL,
    size         INTEGER NOT NULL,
    container    TEXT NOT NULL,
    parse_error  TEXT,
    error_offset INTEGER,
    PRIMARY KEY (batch_id, path)
);
CREATE TABLE IF NOT EXISTS issues (
    issue_id     INTEGER PRIMARY KEY AUTOINCREMENT,
    batch_id     TEXT NOT NULL REFERENCES batches(batch_id) ON DELETE CASCADE,
    path         TEXT,
    track_id     INTEGER,
    severity     TEXT NOT NULL,
    code         TEXT NOT NULL,
    rule         TEXT NOT NULL,
    message      TEXT NOT NULL,
    deviation_ms INTEGER,
    expected     TEXT,
    actual       TEXT
);
CREATE INDEX IF NOT EXISTS issues_by_batch ON issues(batch_id);
"""


def canonical_json(value: Any) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False)


def compute_batch_id(containers: list[Container], options: dict[str, Any],
                     rules_version: str = RULES_VERSION) -> str:
    """Deterministic batch identity: input hashes plus rule options.

    Re-running the same command over unchanged inputs therefore targets the
    same batch row instead of accumulating duplicates.
    """

    payload = {
        "rules_version": rules_version,
        "options": options,
        "inputs": sorted(
            ({"path": c.path, "sha256": c.sha256, "size": c.size} for c in containers),
            key=lambda item: item["path"],
        ),
    }
    return hashlib.sha256(canonical_json(payload).encode("utf-8")).hexdigest()


def _issue_rows(batch_id: str, issues: list[Issue]) -> list[tuple]:
    rows = []
    for issue in issues:
        rows.append((
            batch_id, issue.path, issue.track_id, issue.severity, issue.code,
            issue.rule, issue.message, issue.deviation_ms,
            None if issue.expected is None else canonical_json(issue.expected),
            None if issue.actual is None else canonical_json(issue.actual),
        ))
    return rows


class Database:
    """Thin transactional wrapper; callers decide what a batch contains."""

    def __init__(self, path: str | Path) -> None:
        self.path = str(path)
        self.conn = sqlite3.connect(self.path, isolation_level=None)
        self.conn.row_factory = sqlite3.Row
        self.conn.execute("PRAGMA foreign_keys = ON")
        self.conn.execute("PRAGMA journal_mode = WAL")
        self.conn.executescript(SCHEMA)

    def close(self) -> None:
        self.conn.close()

    def __enter__(self) -> "Database":
        return self

    def __exit__(self, *exc_info: object) -> None:
        self.close()

    def save_batch(self, containers: list[Container], report: BatchReport,
                   options: dict[str, Any], batch_id: str | None = None,
                   rules_version: str = RULES_VERSION) -> str:
        """Replace a batch atomically; on failure nothing of it survives."""

        batch_id = batch_id or compute_batch_id(containers, options, rules_version)
        file_rows = [
            (
                batch_id, c.path, c.sha256, c.size,
                canonical_json(c.to_dict()), c.error, c.error_offset,
            )
            for c in sorted(containers, key=lambda c: c.path)
        ]
        issue_rows = _issue_rows(
            batch_id, sorted(report.issues, key=lambda i: (
                i.path or "", i.track_id if i.track_id is not None else -1,
                i.code, i.message,
            ))
        )
        inputs = [
            {"path": c.path, "sha256": c.sha256, "size": c.size}
            for c in sorted(containers, key=lambda c: c.path)
        ]
        created_at = datetime.now(timezone.utc).isoformat(timespec="seconds")
        try:
            self.conn.execute("BEGIN IMMEDIATE")
            self.conn.execute("DELETE FROM batches WHERE batch_id = ?", (batch_id,))
            self.conn.execute(
                "INSERT INTO batches (batch_id, created_at, rules_version, options, inputs)"
                " VALUES (?, ?, ?, ?, ?)",
                (batch_id, created_at, rules_version, canonical_json(options),
                 canonical_json(inputs)),
            )
            self.conn.executemany(
                "INSERT INTO files (batch_id, path, sha256, size, container,"
                " parse_error, error_offset) VALUES (?, ?, ?, ?, ?, ?, ?)",
                file_rows,
            )
            self.conn.executemany(
                "INSERT INTO issues (batch_id, path, track_id, severity, code, rule,"
                " message, deviation_ms, expected, actual)"
                " VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
                issue_rows,
            )
            self.conn.execute("COMMIT")
        except Exception:
            self.conn.execute("ROLLBACK")
            raise
        return batch_id

    def load_batch(self, batch_id: str) -> tuple[list[Container], list[Issue]] | None:
        with closing(self.conn.execute(
            "SELECT container FROM files WHERE batch_id = ? ORDER BY path", (batch_id,)
        )) as cursor:
            file_rows = cursor.fetchall()
        if not file_rows:
            return None
        containers = [Container.from_dict(json.loads(row["container"])) for row in file_rows]
        with closing(self.conn.execute(
            "SELECT * FROM issues WHERE batch_id = ? ORDER BY path, code, message",
            (batch_id,),
        )) as cursor:
            issue_rows = cursor.fetchall()
        issues = [
            Issue(
                severity=row["severity"], code=row["code"], rule=row["rule"],
                message=row["message"], path=row["path"], track_id=row["track_id"],
                expected=None if row["expected"] is None else json.loads(row["expected"]),
                actual=None if row["actual"] is None else json.loads(row["actual"]),
                deviation_ms=row["deviation_ms"],
            )
            for row in issue_rows
        ]
        return containers, issues

    def latest_batch(self) -> sqlite3.Row | None:
        with closing(self.conn.execute(
            "SELECT * FROM batches ORDER BY created_at DESC, batch_id DESC LIMIT 1"
        )) as cursor:
            return cursor.fetchone()

    def batch_count(self) -> int:
        with closing(self.conn.execute("SELECT COUNT(*) AS n FROM batches")) as cursor:
            return int(cursor.fetchone()["n"])

    def issue_count(self, batch_id: str) -> int:
        with closing(self.conn.execute(
            "SELECT COUNT(*) AS n FROM issues WHERE batch_id = ?", (batch_id,)
        )) as cursor:
            return int(cursor.fetchone()["n"])

    def check_integrity(self) -> str:
        with closing(self.conn.execute("PRAGMA integrity_check")) as cursor:
            return str(cursor.fetchone()[0])


def containers_from_rows(rows: list[sqlite3.Row]) -> list[Container]:
    return [Container.from_dict(json.loads(row["container"])) for row in rows]


__all__ = [
    "Database", "compute_batch_id", "canonical_json", "containers_from_rows",
    "Track",
]
