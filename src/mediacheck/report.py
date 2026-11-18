"""JSON payload assembly and terminal summary."""

from __future__ import annotations

from . import __version__
from .compare import CompareReport


def build_compare_payload(report: CompareReport) -> dict:
    files = [
        {
            "path": f.path,
            "container": f.actual.container,
            "size": f.actual.size,
            "truncated": f.actual.truncated,
            "declared_duration_ms": f.actual.declared_duration_ms,
            "track_count": len(f.actual.tracks),
            "tracks": [t.to_dict() for t in f.actual.tracks],
            "mismatch_count": len(f.mismatches),
        }
        for f in report.files
    ]
    mismatches = [m.to_dict() for m in report.mismatches]
    return {
        "tool": "mediacheck",
        "tool_version": __version__,
        "summary": {
            "files_scanned": report.files_scanned,
            "tracks_expected": report.tracks_expected,
            "tracks_actual": report.tracks_actual,
            "duration_deviation_max_ms": report.duration_deviation_max_ms,
            "mismatches": len(mismatches),
            "errors": len(report.errors),
            "duration_tolerance_ms": report.duration_tolerance_ms,
        },
        "files": files,
        "tracks": [
            {"path": f.path, **t.to_dict()}
            for f in report.files
            for t in f.actual.tracks
        ],
        "mismatches": mismatches,
        "errors": report.errors,
    }


def print_summary(payload: dict) -> None:
    summary = payload.get("summary", {})
    for key, value in summary.items():
        print(f"{key}: {value}")
    for error in payload.get("errors", []):
        print(f"error: {error.get('path')}: {error.get('detail')}")
    for mismatch in payload.get("mismatches", []):
        print(
            f"mismatch: [{mismatch.get('code')}] {mismatch.get('path')}: "
            f"{mismatch.get('detail')}"
        )
