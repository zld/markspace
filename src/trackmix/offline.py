"""Offline (whole-timeline) evaluation of the shared mixing graph."""

from __future__ import annotations

from pathlib import Path

import numpy as np

from . import metrics
from .graph import Graph, TrackNode
from .io_utils import write_render_bundle


def collect_track_metrics(graph: Graph) -> list[dict]:
    """Per-track peak/RMS measured after automation, before the bus sum."""
    report: list[dict] = []
    for node in graph.order:
        if not isinstance(node, TrackNode):
            continue
        chunks = node.runtime.metric_chunks
        data = (
            np.concatenate(chunks)
            if chunks
            else np.empty((0, node.bus_channels), dtype=np.float64)
        )
        report.append(
            {
                "id": node.id.split(":", 1)[1],
                "peak_dbfs": metrics.json_safe(metrics.peak_dbfs(data)),
                "rms_dbfs": metrics.json_safe(metrics.rms_dbfs(data)),
                "frames": int(data.shape[0]),
            }
        )
    return report


def evaluate_offline(graph: Graph, total_frames: int,
                     target_rate: int) -> tuple[np.ndarray, dict]:
    """Evaluate the whole timeline and build its metrics report.

    No files are touched here, so callers can add a streaming check to
    the report before anything is persisted.
    """
    graph.reset()
    master = graph.evaluate_block(0, total_frames)
    track_report = collect_track_metrics(graph)
    clip = metrics.clipping_stats(master)
    report = {
        "target_rate": target_rate,
        "total_frames": total_frames,
        "channels": master.shape[1],
        "tracks": track_report,
        "clipping": {
            "sample_count": clip["sample_count"],
            "peak": clip["peak"],
            "peak_dbfs": metrics.json_safe(
                metrics.peak_dbfs(np.asarray([[clip["peak"]]]))
            ),
        },
    }
    return master, report


def run_offline(
    graph: Graph,
    total_frames: int,
    target_rate: int,
    output_dir: Path,
    stem: str,
    force: bool,
) -> tuple[np.ndarray, dict]:
    """Evaluate the entire timeline in one block and persist outputs."""
    master, report = evaluate_offline(graph, total_frames, target_rate)
    write_render_bundle(
        Path(output_dir), stem, master, target_rate, report, force
    )
    return master, report
