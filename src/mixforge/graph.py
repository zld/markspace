"""Mixing graph: track input, gain automation, sum and master nodes.

The graph is a fixed four-stage DAG. Both renderers (offline and streaming)
call :meth:`MixGraph.render_range`, which is the single graph evaluation
implementation — block boundaries never change the math.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np

from . import automation
from .model import MixConfig, TrackSpec


class GraphError(RuntimeError):
    """Raised when graph wiring or channel counts are inconsistent."""


class Node:
    """Base class for graph nodes, identified by a unique name."""

    kind = "node"

    def __init__(self, name: str):
        self.name = name

    def describe(self) -> dict:
        return {"name": self.name, "kind": self.kind}


class TrackInputNode(Node):
    """Holds a track's timeline-aligned buffer and reads ranges from it."""

    kind = "track_input"

    def __init__(self, name: str, track: TrackSpec, buffer: np.ndarray):
        super().__init__(name)
        self.track = track
        self.buffer = buffer

    def process(self, start: int, end: int) -> np.ndarray:
        return self.buffer[start:end, :].copy()


class GainNode(Node):
    """Applies static trim multiplied by the automation curve (if any)."""

    kind = "gain"

    def __init__(self, name: str, track: TrackSpec, sample_rate: int):
        super().__init__(name)
        self.track = track
        self.sample_rate = sample_rate

    def process(self, signal: np.ndarray, start: int) -> np.ndarray:
        count = signal.shape[0]
        if self.track.automation is None:
            curve_gain = np.ones(count, dtype=np.float64)
        else:
            curve_gain = automation.gain_for_frames(
                self.track.automation, start, count, self.sample_rate
            )
        return signal * (self.track.gain * curve_gain)[:, np.newaxis]


class SumNode(Node):
    """Maps per-track signals onto output channels and accumulates them."""

    kind = "sum"

    def __init__(self, name: str, channels: int):
        super().__init__(name)
        self.channels = channels

    def process(self, signals: list[np.ndarray], tracks, count: int) -> np.ndarray:
        mixed = np.zeros((count, self.channels), dtype=np.float64)
        for track, signal in zip(tracks, signals):
            for src, dst in track.channel_map:
                mixed[:, dst] += signal[:, src]
        return mixed


class MasterNode(Node):
    """Master output, optionally hard-limiting to the [-1, 1] range."""

    kind = "master"

    def __init__(self, name: str, limiter: bool):
        super().__init__(name)
        self.limiter = limiter

    def process(self, signal: np.ndarray) -> tuple[np.ndarray, np.ndarray]:
        limited = np.clip(signal, -1.0, 1.0) if self.limiter else signal.copy()
        return signal, limited


@dataclass(frozen=True)
class BlockResult:
    start: int
    end: int
    pre_master: np.ndarray
    output: np.ndarray
    track_signals: tuple[np.ndarray, ...]


class MixGraph:
    """Validated DAG of :class:`Node` objects evaluated over sample ranges."""

    def __init__(self, config: MixConfig, buffers: list[np.ndarray], total_frames: int):
        if len(buffers) != len(config.tracks):
            raise GraphError("one timeline buffer is required per track")
        self.config = config
        self.total_frames = total_frames
        self.input_nodes: list[TrackInputNode] = []
        self.gain_nodes: list[GainNode] = []
        for index, (track, buffer) in enumerate(zip(config.tracks, buffers)):
            if buffer.shape[1] != track.in_channels:
                raise GraphError(f"track {track.name}: buffer channel mismatch")
            self._validate_channel_map(track)
            self.input_nodes.append(TrackInputNode(f"input:{track.name}", track, buffer))
            self.gain_nodes.append(GainNode(f"gain:{track.name}", track, config.sample_rate))
        self.sum_node = SumNode("sum:bus", config.channels)
        self.master_node = MasterNode("master:out", config.limiter)
        self._validate()

    def _validate_channel_map(self, track: TrackSpec) -> None:
        for src, dst in track.channel_map:
            if not (0 <= src < track.in_channels):
                raise GraphError(f"track {track.name}: source channel {src} out of range")
            if not (0 <= dst < self.config.channels):
                raise GraphError(f"track {track.name}: output channel {dst} out of range")

    def _validate(self) -> None:
        names = [node.name for node in self.iter_nodes()]
        if len(set(names)) != len(names):
            raise GraphError("graph node names must be unique")
        # Every track input must flow through a gain node into the sum,
        # and the sum must feed exactly one master output.
        if len(self.input_nodes) != len(self.gain_nodes):
            raise GraphError("each track input requires one gain node")
        if self.sum_node.channels != self.config.channels:
            raise GraphError("sum node channel count must match project channels")
        if not 1 <= self.config.channels <= 2:
            raise GraphError("output channel count must be 1 or 2")

    def iter_nodes(self):
        yield from self.input_nodes
        yield from self.gain_nodes
        yield self.sum_node
        yield self.master_node

    def render_range(self, start: int, end: int) -> BlockResult:
        """Evaluate one half-open sample range; the only graph entry point."""

        if not (0 <= start <= end <= self.total_frames):
            raise GraphError(f"range [{start}, {end}) outside [0, {self.total_frames})")
        count = end - start
        track_signals: list[np.ndarray] = []
        for input_node, gain_node in zip(self.input_nodes, self.gain_nodes):
            raw = input_node.process(start, end)
            track_signals.append(gain_node.process(raw, start))
        pre_master = self.sum_node.process(
            track_signals, self.config.tracks, count
        )
        _, limited = self.master_node.process(pre_master)
        return BlockResult(
            start=start,
            end=end,
            pre_master=pre_master,
            output=limited,
            track_signals=tuple(track_signals),
        )
