"""Deterministic JSON reports (batch summary and per-file records).

Every float stored in a report is rounded to a fixed number of decimal
places and checked to be finite.  ``NaN``/``Infinity`` are rejected at
serialisation time: :func:`json.dumps` runs with ``allow_nan=False``, so a
bug can never emit a document that standard JSON parsers refuse.

The report is written atomically (``.part`` + rename) so a crash cannot
leave a truncated JSON file behind.
"""

from __future__ import annotations

import json
import math
import os
import tempfile
from typing import Any

__all__ = [
    "ReportWriteError",
    "build_batch_report",
    "dumps",
    "finite",
    "write_json",
]

ROUND_DECIMALS = 9
SCHEMA_VERSION = 1


class ReportWriteError(Exception):
    """The report could not be written; maps to exit code 3."""


def finite(value: float, *, name: str = "value") -> float:
    """Round to :data:`ROUND_DECIMALS` and fail loudly on non-finite input."""
    if not isinstance(value, (int, float)) or isinstance(value, bool):
        raise TypeError(f"{name} must be a real number, got {type(value).__name__}")
    if not math.isfinite(value):
        raise ValueError(f"{name} is not finite: {value!r}")
    rounded = round(float(value), ROUND_DECIMALS)
    # -0.0 is valid JSON but unpleasant in reports; normalise it.
    return 0.0 if rounded == 0.0 else rounded


def _sanitise(obj: Any, path: str = "$") -> Any:
    if isinstance(obj, float):
        return finite(obj, name=path)
    if isinstance(obj, dict):
        return {str(k): _sanitise(v, f"{path}.{k}") for k, v in obj.items()}
    if isinstance(obj, (list, tuple)):
        return [_sanitise(v, f"{path}[{i}]") for i, v in enumerate(obj)]
    if isinstance(obj, bool) or obj is None or isinstance(obj, (str, int)):
        return obj
    if hasattr(obj, "to_dict"):
        return _sanitise(obj.to_dict(), path)
    raise TypeError(f"unsupported report value at {path}: {type(obj).__name__}")


def dumps(report: Any) -> str:
    """Serialise a report to canonical JSON text (strict, no NaN)."""
    cleaned = _sanitise(report)
    return json.dumps(
        cleaned,
        allow_nan=False,
        ensure_ascii=True,
        indent=2,
        sort_keys=True,
    ) + "\n"


def write_json(path: str, report: Any) -> None:
    """Write ``report`` to ``path`` atomically; raises ReportWriteError."""
    text = dumps(report)
    directory = os.path.dirname(os.path.abspath(path)) or "."
    try:
        os.makedirs(directory, exist_ok=True)
        handle, tmp_path = tempfile.mkstemp(prefix=".audiopipe-report-", suffix=".part", dir=directory)
    except OSError as exc:
        raise ReportWriteError(f"cannot create report file: {exc}") from exc
    try:
        with os.fdopen(handle, "w", encoding="utf-8") as fh:
            fh.write(text)
            fh.flush()
            os.fsync(fh.fileno())
        os.replace(tmp_path, path)
    except OSError as exc:
        try:
            os.unlink(tmp_path)
        except OSError:
            pass
        raise ReportWriteError(f"cannot write report: {exc}") from exc


def build_batch_report(
    *,
    command: str,
    config: dict,
    files: list[dict],
    started_at: str,
    duration_seconds: float,
) -> dict:
    """Assemble the top-level batch document."""
    success = sum(1 for f in files if f.get("status") == "ok")
    failed = len(files) - success
    return {
        "schema_version": SCHEMA_VERSION,
        "command": command,
        "config": config,
        "started_at": started_at,
        "duration_seconds": duration_seconds,
        "summary": {
            "files": len(files),
            "success": success,
            "failed": failed,
        },
        "files": files,
    }
