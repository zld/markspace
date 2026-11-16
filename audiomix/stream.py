"""Streaming executor: renders the mix graph in fixed-size blocks.

Handles the short final block and the end-of-stream flush inside the
graph nodes; the concatenated result matches the offline render.
"""
from __future__ import annotations

import numpy as np

from .model import ProcessError


def render_stream(graph, chunk_frames):
    """Render the graph in `chunk_frames` blocks and return the full mix."""
    if chunk_frames <= 0:
        raise ProcessError(f"chunk_frames must be positive, got {chunk_frames}")
    blocks = []
    while True:
        block = graph.pull(chunk_frames)
        if block.shape[0] == 0:
            break
        blocks.append(block)
    if not blocks:
        return np.zeros((0, 2))
    return np.concatenate(blocks)
