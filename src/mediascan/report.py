"""Stable terminal summary and atomic JSON report writing."""

from __future__ import annotations

import json
import os
import tempfile
from typing import Any


def summary_line(summary: dict[str, Any]) -> str:
    delta = summary.get("max_duration_delta_ms")
    delta_text = "null" if delta is None else str(delta)
    return (
        f"files_scanned={summary.get('files_scanned', 0)}"
        f" tracks_total={summary.get('tracks_total', 0)}"
        f" mismatches={summary.get('mismatches', 0)}"
        f" failures={summary.get('failures', 0)}"
        f" max_duration_delta_ms={delta_text}"
    )


def build_summary(
    files_scanned: int,
    tracks_total: int,
    mismatches: int,
    failures: int,
    max_duration_delta_ms: int | None,
    skipped: int = 0,
    counts: dict[str, int] | None = None,
) -> dict[str, Any]:
    summary: dict[str, Any] = {
        "files_scanned": files_scanned,
        "tracks_total": tracks_total,
        "mismatches": mismatches,
        "failures": failures,
        "skipped": skipped,
        "max_duration_delta_ms": max_duration_delta_ms,
    }
    if counts is not None:
        summary["counts"] = {
            "mismatches_by_category": counts["mismatches_by_category"],
            "failures_by_code": counts["failures_by_code"],
        }
    return summary


def build_report(summary: dict[str, Any], files: list[dict], mismatches: list[dict],
                 failures: list[dict]) -> dict[str, Any]:
    return {
        "summary": summary,
        "files": sorted(files, key=lambda f: f["path"]),
        "mismatches": sorted(
            mismatches,
            key=lambda m: (
                m["file"],
                m["track_index"] if m["track_index"] is not None else -1,
                m["error_code"],
                m.get("field") or "",
            ),
        ),
        "failures": sorted(failures, key=lambda f: f["path"]),
    }


def write_json_atomic(path: str, data: dict[str, Any]) -> None:
    """Write JSON via temp file + fsync + replace; never leave a torn report."""
    directory = os.path.dirname(os.path.abspath(path)) or "."
    os.makedirs(directory, exist_ok=True)
    fd, tmp = tempfile.mkstemp(prefix=".report-", suffix=".json", dir=directory)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as fh:
            json.dump(data, fh, indent=2, sort_keys=False, ensure_ascii=False)
            fh.write("\n")
            fh.flush()
            os.fsync(fh.fileno())
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise


def print_failure(exc: Exception) -> None:
    code = getattr(exc, "code", "ERR_INTERNAL")
    detail = getattr(exc, "detail", str(exc))
    offset = getattr(exc, "offset", None)
    where = f" offset={offset}" if offset is not None else ""
    print(f"[FAIL] {code}{where}: {detail}")


def print_summary(summary: dict[str, Any], failures: list[dict] | None = None) -> None:
    print(summary_line(summary))
    for item in failures or []:
        offset = item.get("error_offset")
        where = f" offset={offset}" if offset is not None else ""
        print(f"[FAIL] {item['path']}: {item['error_code']}{where} "
              f"{item.get('error_detail', '')}".rstrip())
