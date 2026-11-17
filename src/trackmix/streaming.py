"""Streaming (block-wise) evaluation of the same mixing graph.

The streaming engine walks absolute output-frame ranges and asks the
shared graph for one block at a time.  Track nodes keep their resampler
history, fractional phase and automation addressing between calls, so
the concatenation of every block matches the offline render.
"""

from __future__ import annotations

import numpy as np

from .errors import StreamVerifyError
from .graph import Graph

VERIFY_TOLERANCE = 1e-6


def iter_block_ranges(total_frames: int, block_frames: int):
    """Yield ``(start, length)`` pairs covering the timeline exactly."""
    if block_frames <= 0:
        raise ValueError("block_frames must be positive")
    start = 0
    while start < total_frames:
        length = min(block_frames, total_frames - start)
        yield start, length
        start += length


def run_streaming(
    graph: Graph, total_frames: int, block_frames: int
) -> np.ndarray:
    """Render the timeline block by block through ``graph``."""
    graph.reset()
    chunks: list[np.ndarray] = []
    for start, length in iter_block_ranges(total_frames, block_frames):
        chunks.append(graph.evaluate_block(start, length))
    if not chunks:
        return np.empty((0, 0), dtype=np.float64)
    return np.concatenate(chunks, axis=0)


def verify_against_offline(
    offline_master: np.ndarray,
    graph: Graph,
    total_frames: int,
    block_frames: int,
) -> dict:
    """Re-render via streaming and compare to the offline reference."""
    streamed = run_streaming(graph, total_frames, block_frames)
    if streamed.shape != offline_master.shape:
        raise StreamVerifyError(
            f"frame/shape mismatch: offline {offline_master.shape} vs "
            f"streaming {streamed.shape}"
        )
    max_abs_diff = float(np.max(np.abs(offline_master - streamed)))
    result = {
        "frames": int(streamed.shape[0]),
        "block_frames": int(block_frames),
        "max_abs_diff": max_abs_diff,
        "tolerance": VERIFY_TOLERANCE,
    }
    if max_abs_diff > VERIFY_TOLERANCE:
        raise StreamVerifyError(
            f"offline/streaming diff {max_abs_diff:.3e} exceeds tolerance "
            f"{VERIFY_TOLERANCE:.0e}"
        )
    return result
