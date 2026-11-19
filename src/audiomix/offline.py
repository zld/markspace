"""Offline driver: render the whole session in one graph pass.

The offline path asks :class:`MixGraph` for the entire frame range, then
writes one WAV block and returns the statistics. Because it shares both the
graph and the statistics accumulator with the streaming driver, the two are
expected to agree sample for sample (float32 WAV quantisation included).
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

from .artifacts import write_wav
from .errors import TrackError
from .mixer import ClipPolicy, MixGraph
from .stats import RenderStats, signal_stats


@dataclass
class RenderResult:
    """What a driver returns to the CLI."""

    frames: int
    samples: object  # np.ndarray (frames x channels)
    stats: RenderStats


def make_stats(graph: MixGraph) -> RenderStats:
    return RenderStats(
        tracks=[signal_stats(t.spec.name, graph.layout) for t in graph.tracks],
        bus=signal_stats("bus", graph.layout),
        master=signal_stats("master", graph.layout),
    )


def render_offline(graph: MixGraph, policy: ClipPolicy) -> RenderResult:
    """Render every effective frame at once and accumulate all statistics."""
    stats = make_stats(graph)
    total = graph.total_frames()
    try:
        rendered = graph.render_frames(0, total)
    except TrackError:
        raise
    except Exception as exc:  # convert numpy/shape failures to labelled errors
        raise TrackError("session", f"offline render failed: {exc}") from exc
    stats.add_block(rendered["tracks"], rendered["bus"], rendered["master"])
    return RenderResult(frames=total, samples=rendered["master"], stats=stats
                        )


def render_offline_to_file(
    graph: MixGraph, policy: ClipPolicy, output: Path, protected: list[Path]
) -> RenderResult:
    """Render offline, write the WAV atomically, and return the result."""
    result = render_offline(graph, policy)
    write_wav(output, result.samples, graph.project_rate, protected)  # type: ignore[arg-type]
    return result
