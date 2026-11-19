"""Offline rendering: one full-length pass through the shared mix graph."""

from __future__ import annotations

import numpy as np

from .automation import automation_samples
from .errors import ConfigError, ProcessingError
from .graph import MixGraph
from .model import Project
from .stats import Summary, SignalStats, TrackReport

#: Offline renders in fixed chunks so the graph API is exercised exactly like
#: the streaming path; memory stays bounded by the chunk size, not duration.
OFFLINE_CHUNK = 65_536


def validate_block_size(block_size: int) -> int:
    if isinstance(block_size, bool) or not isinstance(block_size, int):
        raise ConfigError("block size must be an integer", location="block_size")
    if block_size <= 0:
        raise ConfigError("block size must be positive", location="block_size")
    return block_size


def render_offline(
    project: Project,
    *,
    block_size: int = OFFLINE_CHUNK,
) -> tuple[np.ndarray, Summary]:
    """Render the whole project and return ``(master, summary)``.

    The summary's automation samples are always keyed to ``block_size`` so
    offline and streaming outputs can be compared at the same coordinates;
    the audio itself is chunked for bounded memory.
    """

    validate_block_size(block_size)
    chunk = OFFLINE_CHUNK
    master = np.zeros((project.frames, project.channels), dtype=np.float64)
    summary = Summary(
        sample_rate=project.sample_rate,
        channels=project.channels,
        frames=project.frames,
        block_size=block_size,
        mode="offline",
    )
    summary.automation_samples = automation_samples(project, block_size)
    track_stats = {track.id: SignalStats() for track in project.tracks}
    bus_stats = {bus.id: SignalStats() for bus in project.buses}

    with MixGraph(project) as graph:
        summary.tracks = [
            TrackReport(
                id=runtime.track.id,
                stats=track_stats[runtime.track.id],
                source_sample_rate=runtime.source.sample_rate,
                source_frames=runtime.source.frames,
                clip_frames=runtime.clip_frames,
                start_frame=runtime.track.start_frame,
                mute=runtime.track.mute,
                bus=runtime.track.bus,
                automation_frames=(
                    len(runtime.track.automation.points) if runtime.track.automation else 0
                ),
            )
            for runtime in graph.runtimes
        ]
        for start in range(0, project.frames, chunk):
            frames = min(chunk, project.frames - start)
            block = graph.render_block(start, frames)
            master[start : start + frames] = block.master
            for track_id, data in block.tracks.items():
                track_stats[track_id].feed(data, location=f"track[{track_id}]")
            for bus_id, data in block.buses.items():
                bus_stats[bus_id].feed(data, location=f"bus[{bus_id}]")
            summary.master.feed(block.master_pre_limit, location="master")
    summary.buses = bus_stats
    if not np.all(np.isfinite(master)):
        raise ProcessingError("rendered master contains non-finite samples", location="master")
    summary.master.feed(master, location="master")
    return master, summary
