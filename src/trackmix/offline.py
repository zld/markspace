"""Offline (whole-timeline) evaluation of the shared mixing graph."""

from __future__ import annotations

from pathlib import Path

import numpy as np

from . import metrics
from .graph import Graph, TrackNode
from .io_utils import write_json, write_wav


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


def run_offline(
    graph: Graph,
    total_frames: int,
    target_rate: int,
    output_dir: Path,
    stem: str,
    force: bool,
) -> tuple[np.ndarray, dict]:
    """Evaluate the entire timeline in one block and persist outputs.

    Returns the master mix (unclipped float64) and the metrics report.
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

    output_dir = Path(output_dir)
    wav_path = output_dir / f"{stem}.wav"
    json_path = output_dir / f"{stem}.json"
    write_wav(wav_path, master, target_rate, force)
    write_json(json_path, report, force)
    return master, report
