"""Offline and streaming rendering over one shared mix graph.

Both paths call ``MixGraph.render_block`` with absolute frame positions. The
streaming path only changes how work is partitioned; samples are placed back
into absolute positions, so the two renders are bit-identical. The comparison
still reports the maximum absolute error and the first differing sample.
"""

from __future__ import annotations

from dataclasses import dataclass, field

import numpy as np

from .errors import ConsistencyError, ManifestError, RenderError
from .graph import MixGraph
from .metrics import StatsAccumulator

_MEMORY_BUDGET_BYTES = 1024 * 1024 * 1024


@dataclass
class StreamResult:
    master: np.ndarray | None
    tracks: dict[str, np.ndarray] = field(default_factory=dict)
    block_lengths: list[int] = field(default_factory=list)
    master_stats: StatsAccumulator = field(default_factory=StatsAccumulator)
    track_stats: dict[str, StatsAccumulator] = field(default_factory=dict)


@dataclass
class Comparison:
    max_error: float
    first_diff_frame: int | None
    first_diff_channel: int | None

    @property
    def consistent(self) -> bool:
        return self.max_error < 1e-6

    def as_dict(self) -> dict:
        return {
            "offline_stream_max_error": self.max_error,
            "first_diff_frame": self.first_diff_frame,
            "first_diff_channel": self.first_diff_channel,
            "consistent": self.consistent,
        }


def validate_block_frames(block_frames: int) -> int:
    if isinstance(block_frames, bool) or not isinstance(block_frames, int):
        raise ManifestError("block-frames must be an integer", field="--block-frames")
    if block_frames <= 0:
        raise ManifestError("block-frames must be > 0", field="--block-frames")
    return block_frames


def render_offline(graph: MixGraph) -> tuple[np.ndarray, dict[str, np.ndarray]]:
    """Render the whole session as one block."""
    result = graph.render_block(0, graph.total_frames)
    return result.master, result.tracks


def render_streaming(graph: MixGraph, block_frames: int,
                     *, keep_tracks: bool = True) -> StreamResult:
    """Render block by block; the final block may be shorter.

    ``keep_tracks=False`` accumulates per-track statistics without retaining
    the full per-track buffers.
    """
    block_frames = validate_block_frames(block_frames)
    master = np.zeros((graph.total_frames, 2), dtype=np.float64)
    tracks = ({p.track.id: np.zeros((graph.total_frames, 2), dtype=np.float64)
               for p in graph.programs} if keep_tracks else {})
    master_stats = StatsAccumulator()
    track_stats = {p.track.id: StatsAccumulator() for p in graph.programs}
    lengths: list[int] = []
    for start in range(0, graph.total_frames, block_frames):
        frames = min(block_frames, graph.total_frames - start)
        result = graph.render_block(start, frames)
        master[start:start + frames] = result.master
        master_stats.add(result.master)
        for track_id, samples in result.tracks.items():
            if keep_tracks:
                tracks[track_id][start:start + frames] = samples
            track_stats[track_id].add(samples)
        lengths.append(frames)
    return StreamResult(master=master, tracks=tracks, block_lengths=lengths,
                        master_stats=master_stats, track_stats=track_stats)


def compare(offline: np.ndarray, streaming: np.ndarray) -> Comparison:
    """Max absolute error and the first differing sample between renders."""
    a = np.asarray(offline, dtype=np.float64)
    b = np.asarray(streaming, dtype=np.float64)
    if a.shape != b.shape:
        raise ConsistencyError(
            f"shape mismatch offline {a.shape} vs streaming {b.shape}",
            field="render")
    if a.size == 0:
        return Comparison(0.0, None, None)
    diff = np.abs(a - b)
    max_error = float(np.max(diff))
    if max_error == 0.0:
        return Comparison(0.0, None, None)
    frame, channel = np.unravel_index(int(np.argmax(diff > 0.0)), diff.shape)
    return Comparison(max_error, int(frame), int(channel))


def statistics(tracks: dict[str, np.ndarray], master: np.ndarray) -> dict:
    """Aggregate per-track and master statistics for the offline render."""
    track_stats: dict[str, dict] = {}
    for track_id, samples in tracks.items():
        acc = StatsAccumulator()
        acc.add(samples)
        track_stats[track_id] = acc.as_dict()
    master_acc = StatsAccumulator()
    master_acc.add(master)
    return {"tracks": track_stats, "master": master_acc.as_dict()}


def _check_memory_budget(graph: MixGraph, streaming: bool) -> None:
    """Refuse sessions whose full-length buffers would exhaust the machine."""
    bytes_per_frame = 2 * 8 * (len(graph.programs) + 1 + (1 if streaming else 0))
    needed = graph.total_frames * bytes_per_frame
    if needed > _MEMORY_BUDGET_BYTES:
        raise ManifestError(
            f"session of {graph.total_frames} frames needs about "
            f"{needed / 2**20:.0f} MiB of render buffers, above the "
            f"{_MEMORY_BUDGET_BYTES / 2**20:.0f} MiB budget; shorten duration_ms "
            f"or lower the output rate",
            field="duration_ms",
        )


def process(session, rate: int, block_frames: int | None) -> dict:
    """Render the requested path(s) and return audio, stats and comparison."""
    graph = MixGraph(session, rate)
    _check_memory_budget(graph, streaming=block_frames is not None)
    offline_master, offline_tracks = render_offline(graph)
    comparison = None
    stream = None
    if block_frames is not None:
        block_frames = validate_block_frames(block_frames)
        stream = render_streaming(graph, block_frames, keep_tracks=False)
        comparison = compare(offline_master, stream.master)
        if not comparison.consistent:
            raise ConsistencyError(
                f"offline and streaming differ by {comparison.max_error:.3e} "
                f"at frame {comparison.first_diff_frame} "
                f"channel {comparison.first_diff_channel}",
                field="render",
            )
        del stream.master  # the offline master is the delivered audio
    return {
        "graph": graph,
        "master": offline_master,
        "tracks": offline_tracks,
        "stats": statistics(offline_tracks, offline_master),
        "stream": stream,
        "comparison": comparison,
    }
