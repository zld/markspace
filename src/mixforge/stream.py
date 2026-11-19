"""Streaming rendering: block-by-block, never holding the full mix."""

from __future__ import annotations

from typing import Iterator

import numpy as np

from .automation import automation_samples
from .errors import AudioIOError
from .graph import BlockResult, MixGraph
from .model import Project
from .render import validate_block_size
from .stats import Summary, SignalStats, TrackReport


def iter_blocks(
    project: Project, block_size: int, *, graph: MixGraph | None = None
) -> Iterator[BlockResult]:
    """Yield blocks in timeline order, including a short final block."""

    validate_block_size(block_size)
    own_graph = graph is None
    active = MixGraph(project) if own_graph else graph
    try:
        start = 0
        while start < project.frames:
            frames = min(block_size, project.frames - start)
            try:
                yield active.render_block(start, frames)
            except AudioIOError as exc:
                exc.context.setdefault("block_start", start)
                exc.context.setdefault("block_frames", frames)
                raise
            start += frames
    finally:
        if own_graph:
            active.close()


def stream_render(
    project: Project,
    *,
    block_size: int,
) -> tuple[Iterator[np.ndarray], Summary]:
    """Return a lazy master-block iterator plus a live-updating summary.

    Callers must consume the iterator to completion before trusting the
    summary; no full-length buffer is allocated here.
    """

    block_size = validate_block_size(block_size)
    summary = Summary(
        sample_rate=project.sample_rate,
        channels=project.channels,
        frames=project.frames,
        block_size=block_size,
        mode="stream",
    )
    summary.automation_samples = automation_samples(project, block_size)
    graph = MixGraph(project)
    track_stats = {track.id: SignalStats() for track in project.tracks}
    bus_stats = {bus.id: SignalStats() for bus in project.buses}
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
    summary.buses = bus_stats

    def blocks() -> Iterator[np.ndarray]:
        try:
            for block in iter_blocks(project, block_size, graph=graph):
                for track_id, data in block.tracks.items():
                    track_stats[track_id].feed(data, location=f"track[{track_id}]")
                for bus_id, data in block.buses.items():
                    bus_stats[bus_id].feed(data, location=f"bus[{bus_id}]")
                summary.master.feed(
                    block.master_pre_limit, location="master"
                )
                summary.extra["streamed_blocks"] = summary.extra.get("streamed_blocks", 0) + 1
                yield block.master
        finally:
            graph.close()

    return blocks(), summary
