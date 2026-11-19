"""Streaming driver: advance the shared graph in fixed-size blocks.

Semantics
---------
* Blocks are ``block_frames`` long and every block is rendered at full length.
  Frames past the session end are zero padding used only for computation: they
  are sliced away before writing and before statistics, so they never reach
  the WAV and never inflate ``valid_frames``.
* Loop position, resampler phase, and automation are all functions of absolute
  project frames, so a block boundary in the middle of a loop or a curve
  changes nothing. This is what lets the streaming and offline outputs match.
* WAV blocks are written through one open handle supplied by the caller, which
  keeps the disk layout identical to the offline single-block write.
"""

from __future__ import annotations

from pathlib import Path
from typing import Iterator

import numpy as np

from .artifacts import wav_block_sink
from .errors import AudioMixError, TrackError
from .mixer import MixGraph
from .offline import RenderResult, make_stats, render_range
from .stats import RenderStats


def iter_block_ranges(total: int, block_frames: int) -> list[tuple[int, int, int]]:
    """Return ``(start, effective_end, render_end)`` for each full block.

    ``effective_end`` is clamped to ``total``; ``render_end`` always covers a
    full block, so the final block is zero-padded for computation only.
    """
    if block_frames <= 0:
        raise AudioMixError(f"block_frames must be positive, got {block_frames}")
    total = max(0, total)
    ranges: list[tuple[int, int, int]] = []
    start = 0
    while start < total:
        ranges.append((start, min(start + block_frames, total), start + block_frames))
        start += block_frames
    return ranges


def iter_stream_blocks(
    graph: MixGraph, block_frames: int, stats: RenderStats | None = None
) -> Iterator[np.ndarray]:
    """Yield effective (unpadded) master blocks while accumulating ``stats``."""
    total = graph.total_frames()
    for start, effective_end, render_end in iter_block_ranges(total, block_frames):
        rendered = render_range(graph, start, render_end - start)
        keep = effective_end - start
        block = rendered["master"][:keep]
        if stats is not None:
            stats.add_block(
                {name: values[:keep] for name, values in rendered["tracks"].items()},
                rendered["bus"][:keep],
                block,
            )
        yield block


def render_stream(
    graph: MixGraph,
    block_frames: int,
    output: Path | None = None,
) -> RenderResult:
    """Render the session block by block, optionally writing the WAV."""
    stats = make_stats(graph)
    total = graph.total_frames()
    collected: list[np.ndarray] = []
    written = 0
    if output is not None:
        write_block, close = wav_block_sink(output, graph.layout, graph.project_rate)
        try:
            for block in iter_stream_blocks(graph, block_frames, stats):
                write_block(block)
                written += int(block.shape[0])
        finally:
            close()
    else:
        for block in iter_stream_blocks(graph, block_frames, stats):
            collected.append(block)
            written += int(block.shape[0])
    if written != total:
        raise TrackError("stream", f"wrote {written} frames but session has {total}")
    samples = None if output is not None else (
        np.concatenate(collected, axis=0)
        if collected
        else np.zeros((0, graph.layout), dtype=np.float64)
    )
    return RenderResult(frames=written, samples=samples, stats=stats)
