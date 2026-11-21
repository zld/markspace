"""JSON report assembly, atomic report writing and terminal summaries."""

from __future__ import annotations

import json
import os
import tempfile
from pathlib import Path

from .errors import AssetError
from .mixer import MixGraph


def build_report(graph: MixGraph, mode: str, block_frames: int) -> dict:
    """Collect per-track and bus statistics plus automation summaries."""
    project = graph.project
    tracks = []
    for node in graph.nodes:
        entry = {"name": node.spec.name}
        entry.update(node.stats.to_dict())
        entry["automation_samples"] = node.curve.summary(
            project.automation_interval, project.frames)
        tracks.append(entry)
    return {
        "mode": mode,
        "project_rate": project.project_rate,
        "frames": project.frames,
        "channels": project.channels,
        "block_frames": block_frames,
        "clip_policy": project.clip_policy,
        "clip_threshold": project.clip_threshold,
        "track_count": len(tracks),
        "tracks": tracks,
        "bus": graph.bus_stats.to_dict(),
        "output": str(project.output),
    }


def write_report_atomic(path: str | Path, report: dict) -> None:
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(prefix=f".{path.name}.", suffix=".tmp", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(report, handle, indent=2, sort_keys=True)
            handle.write("\n")
        os.replace(tmp_name, path)
    except OSError as exc:
        try:
            os.unlink(tmp_name)
        except OSError:
            pass
        raise AssetError(f"cannot write report {path}: {exc}") from exc


def format_summary(report: dict) -> str:
    """One ``key=value`` line per required terminal field."""
    lines = [
        f"project_rate={report['project_rate']}",
        f"frames={report['frames']}",
        f"track_count={report['track_count']}",
        f"mode={report['mode']}",
        f"clip_count(bus)={report['bus']['clip_count']}",
    ]
    for track in report["tracks"]:
        name = track["name"]
        lines.append(f"track_peak_dbfs[{name}]={track['peak_dbfs']}")
        lines.append(f"track_rms_dbfs[{name}]={track['rms_dbfs']}")
        lines.append(f"automation_samples[{name}]={len(track['automation_samples'])}")
    lines.append(f"bus_peak_dbfs={report['bus']['peak_dbfs']}")
    lines.append(f"bus_rms_dbfs={report['bus']['rms_dbfs']}")
    return "\n".join(lines)
