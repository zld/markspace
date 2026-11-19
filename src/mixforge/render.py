"""Offline rendering and the shared block-driving engine.

:func:`drive_blocks` is the single engine that walks sample ranges through
:meth:`mixforge.graph.MixGraph.render_range`. Offline rendering requests one
range covering the whole timeline; streaming requests fixed-size ranges —
both execute identical graph code and identical per-sample math.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

import numpy as np
import soundfile as sf

from .graph import BlockResult, MixGraph
from .metrics import Metrics, MetricsAccumulator
from .model import MixConfig, TrackSpec
from .resample import place_track, project_frame_count, resample_track


class RenderError(RuntimeError):
    """Raised when audio data cannot be decoded or processed."""


@dataclass(frozen=True)
class RenderResult:
    output: np.ndarray
    frames: int
    track_metrics: dict[str, Metrics]
    bus_pre_limiter: Metrics
    bus_post_limiter: Metrics


def read_track(track: TrackSpec, project_rate: int) -> np.ndarray:
    """Decode a track to float64 and resample it onto the project rate."""

    try:
        data, sample_rate = sf.read(
            str(track.path), dtype="float64", always_2d=True
        )
    except Exception as exc:
        raise RenderError(f"track {track.name}: failed to decode WAV: {exc}") from exc
    if sample_rate != track.sample_rate:
        raise RenderError(
            f"track {track.name}: sample rate changed between validation and render"
        )
    return resample_track(data, sample_rate, project_rate)


def prepare_graph(config: MixConfig) -> tuple[MixGraph, int]:
    """Decode/resample every track and build the validated mixing graph."""

    total_frames = project_frame_count(config.tracks, config.sample_rate)
    buffers: list[np.ndarray] = []
    for track in config.tracks:
        resampled = read_track(track, config.sample_rate)
        buffers.append(
            place_track(resampled, track, total_frames, config.sample_rate)
        )
    return MixGraph(config, buffers, total_frames), total_frames


def drive_blocks(graph: MixGraph, block_size: int):
    """Yield :class:`BlockResult` objects for successive sample ranges."""

    if block_size <= 0:
        raise RenderError("block size must be a positive integer")
    for start in range(0, graph.total_frames, block_size):
        end = min(start + block_size, graph.total_frames)
        yield graph.render_range(start, end)


def _collect(config: MixConfig, graph: MixGraph, block_size: int) -> RenderResult:
    track_accumulators = {
        track.name: MetricsAccumulator() for track in config.tracks
    }
    pre_accumulator = MetricsAccumulator()
    post_accumulator = MetricsAccumulator()
    chunks: list[np.ndarray] = []
    for block in drive_blocks(graph, block_size):
        chunks.append(block.output)
        for track, signal in zip(config.tracks, block.track_signals):
            track_accumulators[track.name].add(signal)
        pre_accumulator.add(block.pre_master)
        post_accumulator.add(block.output)
    if chunks:
        output = np.concatenate(chunks, axis=0)
    else:
        output = np.zeros((0, config.channels), dtype=np.float64)
    return RenderResult(
        output=output,
        frames=graph.total_frames,
        track_metrics={
            name: accumulator.result()
            for name, accumulator in track_accumulators.items()
        },
        bus_pre_limiter=pre_accumulator.result(),
        bus_post_limiter=post_accumulator.result(),
    )


def render_offline(config: MixConfig) -> RenderResult:
    """Render the whole timeline as a single graph evaluation range."""

    graph, total_frames = prepare_graph(config)
    return _collect(config, graph, max(total_frames, 1))


def render(config: MixConfig, block_size: int) -> RenderResult:
    """Render with an explicit block size (used by the streaming path)."""

    graph, _ = prepare_graph(config)
    return _collect(config, graph, block_size)


def write_wav(path: str | Path, data: np.ndarray, sample_rate: int) -> None:
    """Write a 32-bit float WAV, creating parent directories as needed."""

    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    sf.write(str(path), data, sample_rate, format="WAV", subtype="FLOAT")
