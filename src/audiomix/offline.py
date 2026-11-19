"""Offline driver: render the whole session in one graph pass.

The offline path asks :class:`MixGraph` for the entire frame range, then
writes one WAV block and returns the statistics. Because it shares both the
graph and the statistics accumulator with the streaming driver, the two are
expected to agree sample for sample (float32 WAV quantisation included).

The driver writes into a caller-provided path. The CLI stages that path as a
temporary file and commits WAV + report together, so a failure never leaves a
truncated artifact behind.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

import numpy as np

from .artifacts import write_wav_direct
from .errors import AudioMixError, TrackError
from .mixer import ClipPolicy, MixGraph
from .stats import RenderStats, signal_stats


@dataclass
class RenderResult:
    """What a driver returns to the CLI."""

    frames: int
    samples: np.ndarray | None
    stats: RenderStats


def make_stats(graph: MixGraph) -> RenderStats:
    """Fresh per-track, bus, and master accumulators for a render."""
    return RenderStats(
        tracks=[signal_stats(t.spec.name, graph.layout) for t in graph.tracks],
        bus=signal_stats("bus", graph.layout),
        master=signal_stats("master", graph.layout),
    )


def render_range(graph: MixGraph, start: int, count: int) -> dict:
    """Call the graph, converting unexpected failures into labelled errors."""
    try:
        return graph.render_frames(start, count)
    except AudioMixError:
        raise
    except Exception as exc:
        raise TrackError("session", f"render failed at frame {start}: {exc}") from exc


def render_offline(graph: MixGraph, policy: ClipPolicy) -> RenderResult:
    """Render every effective frame at once and accumulate all statistics."""
    stats = make_stats(graph)
    total = graph.total_frames()
    rendered = render_range(graph, 0, total)
    stats.add_block(rendered["tracks"], rendered["bus"], rendered["master"])
    return RenderResult(frames=total, samples=rendered["master"], stats=stats)


def render_offline_to_file(graph: MixGraph, output: Path) -> RenderResult:
    """Render offline and write the WAV to exactly ``output``."""
    result = render_offline(graph, graph.clip)
    assert result.samples is not None
    write_wav_direct(output, result.samples, graph.project_rate)
    return result
