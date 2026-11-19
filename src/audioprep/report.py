"""Batch report assembly and atomic JSON persistence.

The report records the algorithm parameters, per-file relative paths,
pre/post-normalization measurements and failure details (stage + error type),
sorted by relative path for stable diffs. Written via temp file + rename so
consumers never observe a partial report.
"""
from __future__ import annotations

import json
import os
import tempfile
from pathlib import Path

from . import VERSION

TOOL_NAME = "audioprep"


def build_report(params: dict, entries: list[dict],
                 warnings: list[dict] | None = None) -> dict:
    """Assemble the report document; entries sorted by relative path."""
    files = sorted(entries, key=lambda e: e["relpath"])
    succeeded = sum(1 for e in files if e["status"] == "ok")
    return {
        "tool": TOOL_NAME,
        "version": VERSION,
        "params": dict(params),
        "summary": {
            "total": len(files),
            "succeeded": succeeded,
            "failed": len(files) - succeeded,
        },
        "warnings": list(warnings or []),
        "files": files,
    }


def dumps_report(report: dict) -> str:
    """Deterministic JSON; rejects non-finite floats instead of emitting them."""
    return json.dumps(report, indent=2, sort_keys=False, allow_nan=False) + "\n"


def write_report_atomic(report: dict, path: str | Path) -> None:
    """Write report JSON to a temp file, then atomically replace the target."""
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    text = dumps_report(report)
    fd, tmp = tempfile.mkstemp(dir=path.parent,
                               prefix=f".{path.name}.tmp-", suffix=".json")
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as fh:
            fh.write(text)
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise
