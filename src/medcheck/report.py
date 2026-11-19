"""Terminal summary and JSON report rendering.

The JSON document uses a stable field order (insertion order, no sorting)
so repeated runs over the same inputs produce byte-identical reports.
"""

from __future__ import annotations

import json
import sys


def codec_summary_of(record: dict) -> str:
    parts = []
    for track in record.get("tracks", []):
        bits = [track.get("kind") or "?", track.get("codec") or "?"]
        if track.get("sample_rate"):
            bits.append(f"{track['sample_rate']}Hz")
        if track.get("channels"):
            bits.append(f"{track['channels']}ch")
        if track.get("width") and track.get("height"):
            bits.append(f"{track['width']}x{track['height']}")
        parts.append("/".join(str(b) for b in bits))
    return ",".join(parts)


def build_report(results: list[dict]) -> dict:
    """Assemble the JSON report from per-file scan results.

    Each result is {"record", "mismatches", "duration_delta_ms"}.
    """
    files = []
    all_mismatches = []
    all_errors = []
    track_count = 0
    max_delta = 0.0
    codecs: set[str] = set()
    for result in results:
        record = result["record"]
        summary = codec_summary_of(record)
        if summary:
            codecs.add(summary)
        tracks = record.get("tracks", [])
        track_count += len(tracks)
        max_delta = max(max_delta, result["duration_delta_ms"])
        for mismatch in result["mismatches"]:
            all_mismatches.append({"path": record["path"], **mismatch})
        for error in record.get("errors", []):
            all_errors.append({"path": record["path"], "error": error})
        files.append({
            "path": record["path"],
            "format": record.get("format"),
            "tracks": len(tracks),
            "duration_delta_ms": round(result["duration_delta_ms"], 3),
            "codec_summary": summary,
            "mismatches": result["mismatches"],
            "errors": record.get("errors", []),
        })
    return {
        "files": files,
        "track_count": track_count,
        "duration_delta_ms": round(max_delta, 3),
        "codec_summary": sorted(codecs),
        "mismatches": all_mismatches,
        "errors": all_errors,
    }


def print_summary(report: dict, stream=None) -> None:
    stream = stream or sys.stdout
    for entry in report["files"]:
        print(
            f"{entry['path']}: format={entry['format']} "
            f"tracks={entry['tracks']} "
            f"delta={entry['duration_delta_ms']:.1f}ms "
            f"mismatches={len(entry['mismatches'])} "
            f"errors={len(entry['errors'])}",
            file=stream)
    print(
        f"files={len(report['files'])} "
        f"tracks={report['track_count']} "
        f"mismatches={len(report['mismatches'])} "
        f"errors={len(report['errors'])}",
        file=stream)


def write_json(report: dict, path: str) -> None:
    with open(path, "w", encoding="utf-8") as fh:
        json.dump(report, fh, indent=2, ensure_ascii=False)
        fh.write("\n")
