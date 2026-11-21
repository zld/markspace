"""Streaming driver: renders the timeline in fixed-size blocks.

Resampler phase, automation time and the final partial block are all
carried by :class:`audiomix.graph.MixEngine`, so the concatenated output
matches the offline render exactly.  The renderer can be reset and run
again with any (positive) block size.
"""

from __future__ import annotations

import numpy as np

from .graph import MixEngine


class StreamRenderer:
    def __init__(self, engine: MixEngine, block_size: int):
        if block_size <= 0:
            raise ValueError("block_size must be a positive integer")
        self.engine = engine
        self.block_size = block_size

    def render(self) -> np.ndarray:
        """Render the full mix in blocks; returns the pre-limit master."""
        self.engine.reset()
        blocks = []
        g0 = 0
        total = self.engine.total_frames
        while g0 < total:
            n = min(self.block_size, total - g0)
            blocks.append(self.engine.process_block(g0, n))
            g0 += n
        return np.vstack(blocks) if blocks else np.zeros(
            (0, self.engine.channels), dtype=np.float64
        )


def render_stream(engine: MixEngine, block_size: int) -> np.ndarray:
    return StreamRenderer(engine, block_size).render()
