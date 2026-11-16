"""Terminal and JSON reporting.

The terminal summary is always produced before persistence/JSON writes so
an unwritable report path can never swallow the findings.  JSON write
failures raise :class:`ReportError`, mapped by the CLI to exit code 3.
"""

from __future__ import annotations

import json
import os

from .compare import ScanComparison
from .container_probe import ProbeResult
from .tracks import codec_summary


class ReportError(Exception):
    """Raised when the JSON report cannot be written."""


def build_report(results: list[ProbeResult],
                 comparison: ScanComparison,
                 tolerance_ms: int) -> dict:
    """Assemble the JSON-serializable report document."""
    files_ok = sum(1 for r in results if r.ok)
    files_failed = len(results) - files_ok
    all_tracks = [t for r in results for t in r.tracks]
    return {
        "inputs": len(results),
        "files_total": len(results),
        "files_ok": files_ok,
        "files_failed": files_failed,
        "tracks": len(all_tracks),
        "duration_tolerance_ms": tolerance_ms,
        "max_duration_delta_ms": comparison.max_duration_delta_ms,
        "codec_summary": codec_summary(all_tracks),
        "mismatches": [m.to_dict() for m in comparison.mismatches],
        "mismatch_count": len(comparison.mismatches),
        "groups": comparison.to_dict()["groups"],
        "files": [r.to_dict() for r in results],
    }


def terminal_line(report: dict) -> str:
    return (
        f"files_total={report['files_total']} "
        f"files_ok={report['files_ok']} "
        f"files_failed={report['files_failed']} "
        f"tracks={report['tracks']} "
        f"max_duration_delta_ms={report['max_duration_delta_ms']} "
        f"mismatches={report['mismatch_count']}"
    )


def print_terminal(report: dict) -> None:
    print(terminal_line(report))
    for entry in report["files"]:
        if entry["ok"]:
            print(f"  OK   {entry['path']} [{entry['container']}] "
                  f"tracks={len(entry['tracks'])} "
                  f"duration_ms={entry['duration_ms']}")
        else:
            print(f"  FAIL {entry['path']}: {entry['error']}")
    for mismatch in report["mismatches"]:
        print(f"  MISMATCH [{mismatch['kind']}] {mismatch['detail']} "
              f"group={mismatch['group']} files={','.join(mismatch['files'])}")


def ensure_writable(path: str, input_paths: list[str]) -> None:
    """Validate the JSON destination before any work is lost.

    Rejects directories, existing non-regular files, unwritable parents and
    any path that points back at one of the scanned inputs.
    """
    abs_target = os.path.abspath(path)
    for input_path in input_paths:
        if abs_target == os.path.abspath(input_path):
            raise ReportError(
                f"refusing to overwrite input file with report: {path}")
    if os.path.exists(path):
        if os.path.isdir(path) or not os.path.isfile(path):
            raise ReportError(f"report path is not a regular file: {path}")
        if not os.access(path, os.W_OK):
            raise ReportError(f"report path not writable: {path}")
        return
    parent = os.path.dirname(abs_target) or "."
    if not os.path.isdir(parent) or not os.access(parent, os.W_OK):
        raise ReportError(f"report directory not writable: {parent}")


def write_json(path: str, report: dict, input_paths: list[str]) -> None:
    ensure_writable(path, input_paths)
    try:
        with open(path, "w", encoding="utf-8") as f:
            json.dump(report, f, indent=2, sort_keys=True)
            f.write("\n")
    except OSError as exc:
        raise ReportError(f"cannot write JSON report: {exc}") from exc
