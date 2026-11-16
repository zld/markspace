"""Terminal summary and deterministic JSON report rendering."""

from __future__ import annotations

import json
import os
from typing import Any

from .compare import AuditResult
from .probe import MediaInfo


def build_report(
    infos: list[MediaInfo],
    audit: AuditResult,
    tolerance_ms: int,
    errors: list[dict[str, Any]],
) -> dict[str, Any]:
    """Assemble the JSON payload.

    Track order follows each file's parsed track order; files and mismatches
    are sorted by stable keys so repeated runs byte-compare identically.
    """
    sorted_infos = sorted(infos, key=lambda item: item.name)
    files_payload = []
    tracks_payload = []
    codec_summary: dict[str, Any] = {}
    for info in sorted_infos:
        files_payload.append(
            {
                "name": info.name,
                "container": info.container,
                "byte_size": info.byte_size,
                "duration_ms": info.duration_ms,
                "brands": list(info.brands),
                "parse_error": info.parse_error,
                "error_offset": info.error_offset,
                "track_counts": info.track_counts(),
            }
        )
        for index, track in enumerate(info.tracks):
            entry = {
                "file": info.name,
                "index": index,
                "track_id": track.track_id,
                "type": track.type,
                "codec": track.codec,
                "codec_family": track.codec_family,
                "codec_summary": track.codec_summary,
                "language": track.language,
                "hidden": track.hidden,
                "params": track.params,
            }
            tracks_payload.append(entry)
            bucket = codec_summary.setdefault(
                track.type,
                {"families": set(), "summaries": set()},
            )
            bucket["families"].add(track.codec_family)
            bucket["summaries"].add(track.codec_summary)

    payload = {
        "files": files_payload,
        "tracks": tracks_payload,
        "duration_delta_ms": audit.max_duration_delta_ms,
        "duration_tolerance_ms": tolerance_ms,
        "codec_summary": {
            track_type: {
                "families": sorted(data["families"]),
                "summaries": sorted(data["summaries"]),
            }
            for track_type, data in sorted(codec_summary.items())
        },
        "mismatches": [m.to_dict() for m in audit.mismatches],
        "errors": sorted(errors, key=lambda item: (item["file"], item["error"])),
        "skipped": audit.skipped,
        "groups": audit.to_dict()["groups"],
    }
    return payload


def write_json(path: str, payload: dict[str, Any], input_root: str) -> None:
    """Write the report, refusing to overwrite anything in the input tree."""
    target = os.path.abspath(path)
    root = os.path.abspath(input_root)
    if target == root or target.startswith(root + os.sep):
        raise ValueError(
            f"refusing to write report {path} inside input root {input_root}"
        )
    parent = os.path.dirname(target)
    if parent:
        os.makedirs(parent, exist_ok=True)
    with open(target, "w", encoding="utf-8") as handle:
        json.dump(payload, handle, indent=2, sort_keys=False, ensure_ascii=False)
        handle.write("\n")


def render_terminal(
    infos: list[MediaInfo],
    audit: AuditResult,
    errors: list[dict[str, Any]],
    tolerance_ms: int,
) -> str:
    sorted_infos = sorted(infos, key=lambda item: item.name)
    counts = {"video": 0, "audio": 0, "subtitle": 0, "other": 0}
    for info in sorted_infos:
        for key, value in info.track_counts().items():
            counts[key] += value
    total_tracks = sum(counts.values())
    lines = [
        "media-audit summary",
        f"  files={len(sorted_infos)} "
        f"tracks={total_tracks} "
        f"(video={counts['video']}, audio={counts['audio']}, "
        f"subtitle={counts['subtitle']}, other={counts['other']})",
        f"  max_duration_delta_ms={audit.max_duration_delta_ms} "
        f"(tolerance={tolerance_ms}ms)",
        f"  mismatches={len(audit.mismatches)} "
        f"errors={len(errors)} skipped={len(audit.skipped)}",
    ]
    for mismatch in audit.mismatches:
        lines.append(
            f"  - [{mismatch.kind}] {mismatch.group}/{mismatch.file}: "
            f"{mismatch.detail}"
        )
    for error in sorted(errors, key=lambda item: item["file"]):
        offset = (
            f" at offset {error['offset']}"
            if error.get("offset") is not None
            else ""
        )
        lines.append(f"  ! {error['file']}: {error['error']}{offset}")
    for event in audit.skipped:
        lines.append(f"  ~ skipped {event['path']}: {event['reason']}")
    return "\n".join(lines)
