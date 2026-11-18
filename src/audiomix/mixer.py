"""Reusable mixing graph shared by the offline and streaming render paths.

The node order for every track block is fixed and identical regardless of
how the stream is chunked::

    source block -> channel mapping -> mute -> track gain -> automation
                  -> (sum into bus) -> bus gain

Both render paths call :meth:`MixGraph.process_track_block` and
:meth:`MixGraph.apply_bus`, so whole-length and block-boundary results come
from the same arithmetic.  NaN/Inf values are never written: they abort the
render and are located to the owning track.
"""
from __future__ import annotations

import numpy as np

from .automation import AutomationCurve
from .model import RenderError, Timeline


class TrackNode:
    """Fixed-order per-track node chain operating on one output block."""

    def __init__(self, spec, rate: int):
        self.spec = spec
        self.rate = rate
        self.curve = (AutomationCurve(spec.automation)
                      if spec.automation is not None else None)

    def process(self, block: np.ndarray, frame_start: int) -> np.ndarray:
        """Apply mute, track gain and automation to a (n, ch) block.

        Channel mapping is performed earlier, while source frames are read,
        because it depends on the source channel layout.
        """
        if self.spec.muted:
            return np.zeros_like(block)
        out = block * np.float64(self.spec.gain)
        if self.curve is not None:
            indices = np.arange(frame_start, frame_start + block.shape[0],
                                dtype=np.int64)
            gains = self.curve.frame_gains(indices, self.rate)
            out = out * gains[:, np.newaxis]
        bad = ~np.isfinite(out)
        if bad.any():
            kind = "NaN" if np.isnan(out).any() else "infinite"
            frame = frame_start + int(np.argmax(bad.any(axis=1)))
            raise RenderError(
                f"{kind} sample detected in track {self.spec.name!r} at "
                f"output frame {frame}", track=self.spec.name)
        return out


class MixGraph:
    """Holds the immutable session graph and evaluates output blocks."""

    def __init__(self, timeline: Timeline):
        self.timeline = timeline
        self.rate = timeline.output_rate
        self.channels = timeline.channels
        self.nodes = [TrackNode(t, self.rate) for t in timeline.tracks]

    def process_track_block(self, node_index: int, block: np.ndarray,
                            frame_start: int) -> np.ndarray:
        return self.nodes[node_index].process(block, frame_start)

    def mix_tail(self, block: np.ndarray, frame_start: int) -> np.ndarray:
        """Apply the bus gain node and validate the summed bus."""
        out = block * np.float64(self.timeline.bus_gain)
        bad = ~np.isfinite(out)
        if bad.any():
            kind = "NaN" if np.isnan(out).any() else "infinite"
            frame = frame_start + int(np.argmax(bad.any(axis=1)))
            raise RenderError(
                f"{kind} sample detected on mix bus at output frame {frame}")
        return out
