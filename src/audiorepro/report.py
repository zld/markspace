"""Versioned JSON report writing, loading and independent comparison."""

from __future__ import annotations

import datetime as _dt
import json
import math
import os
import tempfile
from pathlib import Path
from typing import Any

from . import REPORT_SCHEMA, __version__
from .errors import OutputError, VerifyError

DB_TOLERANCE_KEYS = (
    "rms_before_dbfs",
    "lufs_before",
    "rms_after_dbfs",
    "lufs_after",
    "peak_before_dbfs",
    "peak_after_dbfs",
)
EXACT_KEYS = (
    "clip_samples_before",
    "clip_samples_after",
    "frames_in",
    "frames_out",
    "channels",
    "sample_rate",
)


def utc_now() -> str:
    return _dt.datetime.now(_dt.timezone.utc).replace(microsecond=0).isoformat()


def canonical_json(value: Any) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), allow_nan=False)


def write_json_atomic(path: Path, payload: dict[str, Any]) -> None:
    """Serialize to a sibling temp file, fsync, then atomically replace."""
    path.parent.mkdir(parents=True, exist_ok=True)
    handle = None
    temp_path = None
    try:
        fd, temp_name = tempfile.mkstemp(prefix=f".{path.name}.", suffix=".tmp", dir=path.parent)
        temp_path = Path(temp_name)
        handle = os.fdopen(fd, "w", encoding="utf-8")
        json.dump(payload, handle, indent=2, sort_keys=True, allow_nan=False)
        handle.write("\n")
        handle.flush()
        os.fsync(handle.fileno())
        handle.close()
        handle = None
        os.replace(temp_path, path)
        temp_path = None
    except (OSError, ValueError, TypeError) as exc:
        raise OutputError(f"cannot write JSON report {path}: {exc}") from exc
    finally:
        if handle is not None:
            handle.close()
        if temp_path is not None:
            try:
                temp_path.unlink()
            except OSError:
                pass


def load_report(path: Path) -> dict[str, Any]:
    try:
        with open(path, "r", encoding="utf-8") as handle:
            data = json.load(handle)
    except OSError as exc:
        raise VerifyError(f"cannot read report {path}: {exc}") from exc
    except json.JSONDecodeError as exc:
        raise VerifyError(f"report {path} is not valid JSON: {exc}") from exc
    if not isinstance(data, dict) or data.get("schema") != REPORT_SCHEMA:
        raise VerifyError(f"report {path} has unsupported schema")
    if not isinstance(data.get("files"), list) or "params" not in data:
        raise VerifyError(f"report {path} is missing required sections")
    return data


def _close(actual: float | None, expected: float | None, tolerance: float) -> bool:
    if expected is None or actual is None:
        return actual is None and expected is None
    if not (math.isfinite(actual) and math.isfinite(expected)):
        return False
    return abs(actual - expected) <= tolerance


def compare_entry(
    recorded: dict[str, Any], recomputed: dict[str, Any], tolerance: float
) -> list[dict[str, Any]]:
    """Return a list of differences between report and recomputed values."""
    diffs: list[dict[str, Any]] = []
    for key in DB_TOLERANCE_KEYS:
        expected = recorded.get(key)
        actual = recomputed.get(key)
        if not _close(actual, expected, tolerance):
            diffs.append(
                {
                    "field": key,
                    "recorded": expected,
                    "recomputed": actual,
                    "delta": _delta(actual, expected),
                }
            )
    for key in EXACT_KEYS:
        expected = recorded.get(key)
        actual = recomputed.get(key)
        if actual != expected:
            diffs.append(
                {"field": key, "recorded": expected, "recomputed": actual, "delta": None}
            )
    return diffs


def _delta(actual: Any, expected: Any) -> float | None:
    try:
        return round(float(actual) - float(expected), 6)
    except (TypeError, ValueError):
        return None


def summarize_verify(
    files: list[dict[str, Any]], tolerance: float, report_path: Path
) -> dict[str, Any]:
    mismatched = [item for item in files if item["diffs"]]
    return {
        "schema": REPORT_SCHEMA,
        "tool_version": __version__,
        "checked_at": utc_now(),
        "report": str(report_path),
        "tolerance": tolerance,
        "files_checked": len(files),
        "files_scored": sum(1 for item in files if item.get("recomputed") is not None),
        "files_with_differences": len(mismatched),
        "ok": not mismatched,
        "files": files,
    }
