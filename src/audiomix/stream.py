"""Streaming driver: advance the shared graph in fixed-size blocks.

Semantics
---------
* Blocks are ``block_frames`` long, and the final block is always rendered at
  full length. Frames past ``total_frames()`` are zero padding used only for
  computation: they are sliced away before writing and before statistics, so
  they never appear in the WAV and never inflate ``valid_frames``.
* Because the graph maps project frames directly to source rows, padding is
  harmless; it exists so a future stateful block processor sees a full block.

Streaming writes WAV blocks incrementally through an atomic temp file, so a
mid-render failure discards the partial artifact.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

import numpy as np

from .artifacts import atomic_path, write_wav_blocked
from .errors import TrackError
from .mixer import ClipPolicy, MixGraph
from .offline import RenderResult, make_stats
from .stats import RenderStats


def iter_block_ranges(total: int, block_frames: int) -> list[tuple[int, int, int]]:
    """Return ``(start, effective_end, render_end)`` for each full block.

    ``effective_end`` is clamped to ``total``; ``render_end`` covers the full
    block, so the last block is zero-padded to ``block_frames`` for
    computation only.
    """
    if block_frames <= 0:
        raise ValueError(f"block_frames must be positive, got {block_frames}")
    total = max(0, total)
    ranges: list[tuple[int, int, int]] = []
    start = 0
    while start < total:
        ranges.append((start, min(start + block_frames, total), start + block_frames))
        start += block_frames
    return ranges


def render_stream(
    graph: MixGraph,
    policy: ClipPolicy,
    block_frames: int,
    output: Path | None = None,
    protected: list[Path] | None = None,
) -> RenderResult:
    """Render the session block by block, optionally writing the WAV.

    When ``output`` is given, blocks are written incrementally and the result's
    ``samples`` is ``None`` to avoid holding the whole mix in memory twice.
    """
    stats = make_stats(graph)
    total = graph.total_frames()
    ranges = iter_block_ranges(total, block_frames)
    collected: list[np.ndarray] = []
    written = 0

    def produce() -> "object":
        nonlocal written
        for start, effective_end, render_end in ranges:
            rendered = graph.render_frames(start, render_end - start)
            block = rendered["master"][: effective_end - start]
            tracks = {
                name: values[: effective_end - start]
                for name, values in rendered["tracks"].items()
            }
            bus = rendered["bus"][: effective_end - start]
            stats.add_block(tracks, bus, block)
            if output is None:
                collected.append(block)
            written += int(block.shape[0])
            yield block

    try:
        if output is not None:
            with atomic_path(output, protected or []) as temp:
                frames = write_wav_blocked(
                    temp, produce(), graph.layout, graph.project_rate
                )
                if frames != total:
                    raise TrackError(
                        "stream", f"wrote {frames} frames but session has {total}"
                    )
        else:
            for _ in produce():
                pass
    except TrackError:
        raise
    except Exception as exc:
        raise TrackError("stream", f"streaming render failed: {exc}") from exc

    samples = None if output is not None else (
        np.concatenate(collected, axis=0)
        if collected
        else np.zeros((0, graph.layout), dtype=np.float64)
    )
    return RenderResult(frames=written, samples=samples, stats=stats)
