"""JSON report construction and terminal summary formatting.

The report is intentionally flat for the fields the acceptance criteria pin
down (``tracks[name].peak/rms/valid_frames/clip_count`` and
``automation`` sample lists) and adds context (project rate, policy, session
length) without changing those keys.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any, Mapping

from .errors import OutputError
from .mixer import ClipPolicy, MixGraph
from .stats import RenderStats


def build_report(
    graph: MixGraph,
    stats: RenderStats,
    rate: int,
    mode: str,
    block_frames: int | None,
    policy: ClipPolicy,
    output_frames: int,
) -> dict[str, Any]:
    """Assemble the report dictionary from graph metadata and run stats."""
    tracks: dict[str, Any] = {}
    automation: dict[str, list[dict[str, Any]]] = {}
    for track in graph.tracks:
        name = track.spec.name
        entry = stats.to_dict()["tracks"][name]  # type: ignore[index]
        entry = dict(entry)  # type: ignore[arg-type]
        entry.update(
            {
                "source_rate": track.source_rate,
                "source_frames": int(track.source.shape[0]),
                "start_frame": track.spec.start_frame,
                "gain_db": track.spec.gain_db,
                "pan": track.spec.pan,
                "loop": None
                if track.loop is None
                else {
                    "start_frame": track.loop.start_frame,
                    "end_frame": track.loop.end_frame,
                },
            }
        )
        tracks[name] = entry
        automation[name] = track.spec.automation.sampled_points(0, max(0, output_frames - 1))
    stats_dict = stats.to_dict()
    return {
        "version": 1,
        "mode": mode,
        "sample_rate": rate,
        "channels": graph.layout,
        "block_frames": block_frames,
        "clip_policy": policy.name,
        "clip_threshold": 1.0,
        "output_frames": output_frames,
        "tracks": tracks,
        "automation": automation,
        "bus": stats_dict["bus"],
        "master": stats_dict["master"],
    }


def write_json(report: Mapping[str, Any], path: Path) -> None:
    """Write deterministic, human-diffable JSON (sorted keys, no NaN)."""
    try:
        text = json.dumps(report, indent=2, sort_keys=True, allow_nan=False)
    except (TypeError, ValueError) as exc:
        raise OutputError(f"report is not strictly JSON-serialisable: {exc}") from exc
    try:
        path.write_text(text + "\n", encoding="utf-8")
    except OSError as exc:
        raise OutputError(f"cannot write report {path}: {exc}") from exc


def format_summary(report: Mapping[str, Any]) -> str:
    """Terminal summary: track count, output frames, clip counts, peaks."""
    tracks = report["tracks"]
    master = report["master"] or {}
    lines = [
        f"mode: {report['mode']}   sample rate: {report['sample_rate']} Hz   "
        f"channels: {report['channels']}",
        f"tracks: {len(tracks)}   output frames: {report['output_frames']}",
    ]
    if report.get("block_frames"):
        lines.append(f"block frames: {report['block_frames']}   policy: {report['clip_policy']}")
    for name, entry in tracks.items():
        lines.append(
            f"  track {name}: peak {entry['peak']:.6f}  rms {entry['rms']:.6f}  "
            f"frames {entry['valid_frames']}  clips {entry['clip_count']}"
        )
    if isinstance(master, Mapping):
        lines.append(
            f"  master: peak {master.get('peak', 0.0):.6f}  rms {master.get('rms', 0.0):.6f}  "
            f"frames {master.get('valid_frames', 0)}  clips {master.get('clip_count', 0)}"
        )
    lines.append(
        f"clip counts: tracks {sum(e['clip_count'] for e in tracks.values())}  "
        f"bus {report['bus']['clip_count'] if report['bus'] else 0}  "
        f"master {master.get('clip_count', 0) if isinstance(master, Mapping) else 0}"
    )
    return "\n".join(lines)
