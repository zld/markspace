"""Terminal summary and JSON report rendering.

JSON output uses stable ordering everywhere: files sorted by path, tracks
by track id, mismatches sorted by kind/track/field, and object keys
sorted, so repeated runs over the same inputs produce identical bytes.
"""
from __future__ import annotations

import json

from .compare import cross_duration_deltas, duration_deltas


def _file_entry(info, mismatches) -> dict:
    tracks = sorted((t.to_dict() for t in info.tracks),
                    key=lambda t: (t["track_id"] is None,
                                   t["track_id"] or 0,
                                   t["media_type"] or ""))
    return {
        "path": info.path,
        "sha256": info.sha256,
        "size": info.size,
        "ok": info.ok,
        "cached": info.cached,
        "tracks": tracks,
        "duration_ms": info.duration_ms,
        "codec": [t["codec"] for t in tracks],
        "codec_summary": info.codec_summary,
        "unknown_boxes": dict(sorted(info.unknown_boxes.items())),
        "mismatches": [m.to_dict() for m in
                       sorted(mismatches, key=lambda m: m.sort_key())],
        "errors": list(info.errors),
    }


def build_report(command: str, results, mismatches_by_path: dict,
                 input_errors: list, reference_path: str | None) -> dict:
    files = []
    total_tracks = 0
    mismatch_count = 0
    deltas: list[float] = []
    ok_results = [r for r in results if r.ok]
    reference = next((r for r in ok_results if r.path == reference_path),
                     None)
    for info in sorted(results, key=lambda r: r.path):
        mismatches = mismatches_by_path.get(info.path, [])
        mismatch_count += len(mismatches)
        total_tracks += len(info.tracks)
        deltas.extend(duration_deltas(info))
        if reference is not None and info.ok and \
                info.path != reference.path:
            deltas.extend(cross_duration_deltas(reference, info))
        files.append(_file_entry(info, mismatches))
    error_count = len(input_errors) + sum(len(r.errors) for r in results)
    return {
        "command": command,
        "reference": reference_path,
        "files": files,
        "errors": list(input_errors),
        "summary": {
            "files": len(results),
            "tracks": total_tracks,
            "duration_delta_ms": round(max(deltas), 3) if deltas else 0,
            "mismatches": mismatch_count,
            "errors": error_count,
        },
    }


def render_terminal(report: dict) -> str:
    lines = [f"== trackaudit {report['command']} =="]
    if report.get("reference"):
        lines.append(f"reference={report['reference']}")
    for entry in report["files"]:
        if entry["ok"]:
            lines.append(
                f"file={entry['path']} tracks={len(entry['tracks'])} "
                f"duration_ms={entry['duration_ms']} "
                f"codec_summary={entry['codec_summary'] or '-'}"
                + (" [cached]" if entry["cached"] else ""))
        else:
            lines.append(f"file={entry['path']} tracks=0 "
                         f"errors={len(entry['errors'])}")
        for error in entry["errors"]:
            lines.append(f"  error: {error['category']} at offset "
                         f"{error['offset']}: {error['message']}")
        for mismatch in entry["mismatches"]:
            parts = [f"  mismatch: {mismatch['kind']}"]
            if "track_id" in mismatch:
                parts.append(f"track_id={mismatch['track_id']}")
            if "media_type" in mismatch:
                parts.append(f"media_type={mismatch['media_type']}")
            if "field" in mismatch:
                parts.append(f"field={mismatch['field']}")
            if "expected" in mismatch:
                parts.append(f"expected={mismatch['expected']}")
            if "actual" in mismatch:
                parts.append(f"actual={mismatch['actual']}")
            lines.append(" ".join(parts))
    for error in report["errors"]:
        lines.append(f"input error: {error.get('path', '?')}: "
                     f"{error.get('error', error)}")
    summary = report["summary"]
    lines.append(
        f"files={summary['files']} tracks={summary['tracks']} "
        f"duration_delta_ms={summary['duration_delta_ms']} "
        f"mismatches={summary['mismatches']} errors={summary['errors']}")
    return "\n".join(lines)


def render_json(report: dict) -> str:
    return json.dumps(report, indent=2, sort_keys=True) + "\n"


def write_json(report: dict, path) -> None:
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(render_json(report))
