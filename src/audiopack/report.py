"""Batch report assembly and atomic JSON serialization."""

from __future__ import annotations

import json
import math
import os
import tempfile
from dataclasses import asdict
from pathlib import Path
from typing import Any

from audiopack.models import BatchSummary, ProcessConfig

REPORT_VERSION = 1


def _sanitize(value: Any) -> Any:
    """Recursively replace non-finite floats (not valid JSON) with None."""
    if isinstance(value, float) and not math.isfinite(value):
        return None
    if isinstance(value, dict):
        return {k: _sanitize(v) for k, v in value.items()}
    if isinstance(value, (list, tuple)):
        return [_sanitize(v) for v in value]
    return value


def build_report(config: ProcessConfig, summary: BatchSummary) -> dict[str, Any]:
    """Assemble the report document; entries sorted by relative path."""
    files = []
    for result in sorted(summary.results, key=lambda r: r.relpath):
        entry: dict[str, Any] = {"path": result.relpath, "status": result.status}
        if result.status == "ok" and result.stats is not None:
            entry["stats"] = asdict(result.stats)
        else:
            entry["error"] = result.error or "unknown error"
        files.append(entry)
    return _sanitize(
        {
            "version": REPORT_VERSION,
            "config": {
                "input_dir": str(config.input_dir),
                "output_dir": str(config.output_dir),
                "sample_rate": config.sample_rate,
                "target_lufs": config.target_lufs,
                "jobs": config.jobs,
                "peak_ceiling_dbfs": config.peak_ceiling_dbfs,
            },
            "summary": {
                "files_ok": summary.files_ok,
                "files_failed": summary.files_failed,
            },
            "files": files,
        }
    )


def write_report_atomic(path: Path, document: dict[str, Any]) -> None:
    """Write JSON via a temporary file plus atomic rename."""
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(prefix=path.name + ".", suffix=".tmp", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as fh:
            json.dump(document, fh, indent=2, sort_keys=False)
            fh.write("\n")
        os.replace(tmp_name, path)
    except Exception:
        try:
            os.unlink(tmp_name)
        except OSError:
            pass
        raise


def load_report(path: Path) -> dict[str, Any]:
    with open(path, encoding="utf-8") as fh:
        return json.load(fh)
