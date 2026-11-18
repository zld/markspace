"""Stable-sorted JSON reports and deterministic terminal summaries."""
from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from .model import ComparisonResult, Media


class ReportWriteError(OSError):
    """Raised when a JSON report cannot be written."""


def codec_summary(media: Media) -> list[dict[str, Any]]:
    """Compact per-track codec parameters, sorted by track id."""
    summary: list[dict[str, Any]] = []
    for track in sorted(media.tracks, key=lambda t: t.track_id):
        entry: dict[str, Any] = {
            "track_id": track.track_id,
            "kind": track.kind_name,
            "codec_id": track.codec_id,
            "language": track.language,
            "timebase": track.timebase,
            "duration_ms": round(track.duration_ms, 3),
        }
        if track.kind_name == "video":
            entry["width"] = track.width
            entry["height"] = track.height
        elif track.kind_name == "audio":
            entry["sample_rate"] = track.sample_rate
            entry["channels"] = track.channels
        if track.unrecognized:
            entry["unrecognized"] = True
            entry["raw_fields"] = track.raw_fields
        summary.append(entry)
    return summary


def build_report(result: ComparisonResult,
                 baseline: Media, candidate: Media) -> dict[str, Any]:
    """Assemble the full JSON payload with stable key/array ordering."""
    payload = result.to_dict()
    payload["input_file"] = result.input_path
    payload["baseline_file"] = result.baseline_path
    payload["codec_summary"] = {
        "baseline": codec_summary(baseline),
        "input": codec_summary(candidate),
    }
    return payload


def write_json_report(path: str | Path, payload: dict[str, Any]) -> None:
    """Atomically write a sorted JSON report."""
    p = Path(path)
    p.parent.mkdir(parents=True, exist_ok=True)
    tmp = p.with_name(f".{p.name}.tmp")
    try:
        with tmp.open("w", encoding="utf-8") as fh:
            json.dump(payload, fh, indent=2, sort_keys=True)
            fh.write("\n")
        p.replace(tmp)
    except OSError as exc:
        try:
            tmp.unlink(missing_ok=True)
        except OSError:
            pass
        raise ReportWriteError(f"cannot write JSON report {p}: {exc}") from exc


def render_summary(result: ComparisonResult) -> str:
    """One-line, parseable terminal summary plus optional mismatch detail."""
    lines = [
        f"baseline_tracks={result.baseline_tracks} "
        f"input_tracks={result.input_tracks} "
        f"duration_delta_ms={round(result.duration_delta_ms, 3):g} "
        f"mismatch_count={len(result.mismatches)} "
        f"error_count={len(result.errors)}"
    ]
    for mismatch in result.mismatches:
        lines.append(f"  - [{mismatch.kind}] {mismatch.message}")
    for error in result.errors:
        loc = f"@offset {error.offset}" if error.offset is not None else ""
        lines.append(f"  - [{error.code}] {error.message} {loc}".rstrip())
    return "\n".join(lines)
