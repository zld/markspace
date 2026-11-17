"""Self-contained directed-acyclic mixing graph.

The graph owns node ordering (Kahn topological sort with cycle
detection) and a shared evaluation protocol: a node is evaluated at most
once per :meth:`Graph.evaluate_block` call and may cache its result.
Offline and streaming entry points construct the *same* graph; only the
block frame ranges differ, while every node keeps state across calls
(resampler history, fractional phase, per-track metric accumulators).

Automation is always addressed by absolute output frame, so no state is
reset at block boundaries.
"""

from __future__ import annotations

from collections import deque
from dataclasses import dataclass, field

import numpy as np

from .automation import GainCurve
from .errors import ManifestError
from .resample import SincResampler


class Node:
    """Base graph node.  Subclasses implement :meth:`evaluate`."""

    def __init__(self, node_id: str):
        self.id = node_id
        self.inputs: list[Node] = []

    def evaluate(self, start: int, length: int) -> np.ndarray:  # pragma: no cover
        raise NotImplementedError


class Graph:
    """A DAG of :class:`Node` objects evaluated in topological order."""

    def __init__(self, nodes: list[Node], output_id: str):
        self.nodes = nodes
        self.by_id = {node.id: node for node in nodes}
        if len(self.by_id) != len(nodes):
            raise ManifestError("mixing graph contains duplicate node ids")
        if output_id not in self.by_id:
            raise ManifestError(f"mixing graph output {output_id!r} is unknown")
        self.order = self._topological_order()
        self.output = self.by_id[output_id]

    def _topological_order(self) -> list[Node]:
        indegree = {node.id: len(node.inputs) for node in self.nodes}
        dependents: dict[str, list[Node]] = {node.id: [] for node in self.nodes}
        for node in self.nodes:
            for upstream in node.inputs:
                if upstream.id not in self.by_id:
                    raise ManifestError(
                        f"node {node.id!r} depends on unknown node {upstream.id!r}"
                    )
                dependents[upstream.id].append(node)
        ready = deque(
            node for node in self.nodes if indegree[node.id] == 0
        )
        order: list[Node] = []
        while ready:
            node = ready.popleft()
            order.append(node)
            for downstream in dependents[node.id]:
                indegree[downstream.id] -= 1
                if indegree[downstream.id] == 0:
                    ready.append(downstream)
        if len(order) != len(self.nodes):
            stuck = [nid for nid, deg in indegree.items() if deg > 0]
            raise ManifestError(f"cycle detected in mixing graph near: {stuck}")
        return order

    def evaluate_block(self, start: int, length: int) -> np.ndarray:
        """Evaluate one block; each node runs at most once, in topo order."""
        cache: dict[str, np.ndarray] = {}
        for node in self.order:
            values = [cache[up.id] for up in node.inputs]
            cache[node.id] = node.evaluate(start, length, *values)
        return cache[self.output.id]

    def reset(self) -> None:
        for node in self.nodes:
            if isinstance(node, TrackNode):
                node.runtime.reset()


# ---------------------------------------------------------------------- #
# Nodes
# ---------------------------------------------------------------------- #
@dataclass
class TrackRuntime:
    """Mutable per-track state shared across offline/streaming blocks."""

    resampler: SincResampler
    start_frame: int
    resampled_len: int
    fed: int = 0
    metric_chunks: list[np.ndarray] = field(default_factory=list)

    def reset(self) -> None:
        self.resampler = SincResampler(
            self.resampler.in_rate,
            self.resampler.out_rate,
            self.resampler.channels,
        )
        self.fed = 0
        self.metric_chunks = []


class TrackNode(Node):
    """Source -> resample -> channel map -> dB automation -> bus frames."""

    def __init__(
        self,
        node_id: str,
        samples: np.ndarray,
        in_rate: int,
        out_rate: int,
        start_frame: int,
        matrix: np.ndarray,
        curve: GainCurve,
        bus_channels: int,
    ):
        super().__init__(node_id)
        self.samples = samples
        self.in_rate = in_rate
        self.out_rate = out_rate
        self.start_frame = start_frame
        self.matrix = matrix
        self.curve = curve
        self.bus_channels = bus_channels
        resampled_len = int(
            np.ceil(samples.shape[0] * out_rate / in_rate)
        )
        self.resampled_len = resampled_len
        self.runtime = TrackRuntime(
            SincResampler(in_rate, out_rate, samples.shape[1]),
            start_frame,
            resampled_len,
        )

    def evaluate(self, start: int, length: int, *ignored: object) -> np.ndarray:
        rt = self.runtime
        end_frame = self.start_frame + self.resampled_len
        local_start = start - self.start_frame
        local_end = start + length - self.start_frame
        overlap = max(0, min(local_end, self.resampled_len)
                      - max(local_start, 0))
        if overlap <= 0:
            return np.zeros((length, self.bus_channels), dtype=np.float64)

        out = np.zeros((length, self.bus_channels), dtype=np.float64)
        first = max(local_start, 0)
        # Clamp both ends: a block may start before the track, end after
        # it, or straddle either boundary.
        last = min(local_end, self.resampled_len)
        n_out = last - first
        if n_out <= 0:
            return out
        needed = rt.resampler.required_input_for(first + n_out)
        if needed > rt.fed:
            real = min(needed, self.samples.shape[0])
            if real > rt.fed:
                rt.resampler.feed(self.samples[rt.fed:real])
                rt.fed = real
            if rt.resampler.fed < needed:  # fixed zero-pad tail rule
                pad = needed - rt.resampler.fed
                rt.resampler.feed(np.zeros((pad, self.samples.shape[1])))
                rt.fed = needed
        resampled = rt.resampler.read(n_out)

        mapped = resampled @ self.matrix  # (n_out, bus_channels)
        abs_frames = np.arange(
            self.start_frame + first, self.start_frame + first + n_out
        )
        gained = mapped * self.curve.sample_linear(abs_frames)[:, None]
        rt.metric_chunks.append(gained.copy())
        slot = first - local_start
        out[slot:slot + n_out] = gained
        return out


class SumNode(Node):
    """Bus: deterministic-order sum of its upstream nodes."""

    def __init__(self, node_id: str, bus_channels: int):
        super().__init__(node_id)
        self.bus_channels = bus_channels

    def evaluate(self, start: int, length: int, *inputs: np.ndarray) -> np.ndarray:
        if not inputs:  # pragma: no cover - graph always has tracks
            return np.zeros((length, self.bus_channels), dtype=np.float64)
        total = np.zeros_like(inputs[0])
        for value in inputs:  # fixed input order from topological sort
            total = total + value
        return total


# ---------------------------------------------------------------------- #
# Graph construction
# ---------------------------------------------------------------------- #
def build_graph(manifest) -> Graph:
    """Build the standard one-bus mixing graph from a parsed manifest."""
    nodes: list[Node] = []
    track_nodes: list[TrackNode] = []
    for track in manifest.tracks:
        node = TrackNode(
            node_id=f"track:{track.id}",
            samples=track.samples,
            in_rate=track.source_rate,
            out_rate=manifest.target_rate,
            start_frame=track.start_frame,
            matrix=track.matrix,
            curve=track.curve,
            bus_channels=manifest.channels,
        )
        nodes.append(node)
        track_nodes.append(node)
    bus = SumNode("bus", manifest.channels)
    bus.inputs = list(track_nodes)
    nodes.append(bus)
    return Graph(nodes, output_id="bus")
