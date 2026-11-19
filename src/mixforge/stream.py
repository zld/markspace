"""Blockwise streaming renderer.

This module deliberately contains no mixing math: it reuses the graph and
block driver from :mod:`mixforge.render`, guaranteeing that streaming output
is bit-identical to offline output for the same session.
"""

from __future__ import annotations

from .graph import MixGraph
from .model import MixConfig
from .render import (
    RenderError,
    RenderResult,
    drive_blocks,
    prepare_graph,
    render,
)


def iter_blocks(graph: MixGraph, block_size: int):
    """Yield per-block results while preserving the automation time base."""

    yield from drive_blocks(graph, block_size)


def render_stream(config: MixConfig, block_size: int) -> RenderResult:
    """Render ``config`` block by block, finishing on the real tail length."""

    return render(config, block_size)


__all__ = [
    "MixGraph",
    "MixConfig",
    "RenderError",
    "RenderResult",
    "iter_blocks",
    "prepare_graph",
    "render_stream",
]
