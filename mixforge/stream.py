"""Chunked execution state shared by offline and streaming renders.

The executor walks the mix graph in fixed-size output blocks.  All
per-block state (resampler filter tails, decoder cursors, automation
positioning) lives inside the graph's track nodes, so a block length
that does not divide the total frame count still terminates cleanly:
the final short block is emitted by ``min(block, remaining)`` here.
"""

from __future__ import annotations

import numpy as np

from .graph import MixGraph


class StreamExecutor:
    """Pull-based block cursor over a :class:`MixGraph`."""

    def __init__(self, graph: MixGraph, block_frames: int):
        if block_frames <= 0:
            raise ValueError("block_frames must be positive")
        self.graph = graph
        self.block_frames = block_frames
        self.position = 0

    @property
    def done(self) -> bool:
        return self.position >= self.graph.frames

    def next_block(self) -> tuple[np.ndarray, list[np.ndarray]] | None:
        """Render the next block, or None once the graph is exhausted."""
        if self.done:
            return None
        n = min(self.block_frames, self.graph.frames - self.position)
        mix, contribs = self.graph.render_block(self.position, n)
        self.position += n
        return mix, contribs
