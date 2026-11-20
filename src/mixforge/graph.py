"""Mix graph: nodes, validation, deterministic block processing.

The graph is a DAG of nodes — track sources, channel adapters, gain
stages, a summing bus and the master output. Both the offline and the
streaming renderers drive the *same* :meth:`MixGraph.execute` block
processing, so their outputs are identical by construction.

Validation rejects dangling dependencies (an input naming a node that
does not exist) and cycles. Processing order is a deterministic
topological sort (Kahn's algorithm with lexicographically ordered ready
set), independent of node insertion order.
"""

from __future__ import annotations

import heapq
from dataclasses import dataclass, field
from pathlib import Path

import numpy as np
import soundfile as sf

from .automation import GainCurve
from .model import AudioIOError, GraphError, Project, Track
from .resample import SincResampler

_READ_CHUNK = 4096


class TrackSource:
    """Lazily decoded, resampled PCM for one track, cached and trimmed."""

    def __init__(self, path: Path, target_rate: int) -> None:
        try:
            self._sf = sf.SoundFile(path)
        except Exception as exc:  # soundfile raises several exception types
            raise AudioIOError(f"cannot open audio file {path}: {exc}") from exc
        if self._sf.channels not in (1, 2):
            self._sf.close()
            raise AudioIOError(
                f"unsupported channel layout in {path}: "
                f"{self._sf.channels} channels (only mono and stereo)"
            )
        self.channels = self._sf.channels
        self._resampler = SincResampler(
            self._sf.samplerate, target_rate, self.channels
        )
        self._cache = np.zeros((0, self.channels), dtype=np.float64)
        self._cache_start = 0
        self._done = False

    def _pump(self) -> None:
        block = self._sf.read(_READ_CHUNK, dtype="float64", always_2d=True)
        if len(block) == 0:
            self._done = True
            self._append(self._resampler.push(np.zeros((0, self.channels)), final=True))
            self._sf.close()
        else:
            self._append(self._resampler.push(block))

    def _append(self, out: np.ndarray) -> None:
        if len(out):
            self._cache = np.concatenate([self._cache, out], axis=0)

    def _ensure(self, end_frame: int) -> None:
        while not self._done and self._cache_start + len(self._cache) < end_frame:
            self._pump()

    def read(self, start: int, count: int) -> np.ndarray:
        """Return ``count`` resampled frames from ``start``, zero-padded."""
        if count <= 0:
            return np.zeros((0, self.channels), dtype=np.float64)
        self._ensure(start + count)
        out = np.zeros((count, self.channels), dtype=np.float64)
        lo = max(start, self._cache_start)
        hi = min(start + count, self._cache_start + len(self._cache))
        if lo < hi:
            out[lo - start : hi - start] = self._cache[
                lo - self._cache_start : hi - self._cache_start
            ]
        return out

    def release_before(self, frame: int) -> None:
        """Drop cached frames below ``frame`` to bound memory."""
        drop = min(frame - self._cache_start, len(self._cache))
        if drop > 0:
            self._cache = self._cache[drop:]
            self._cache_start += drop


@dataclass
class Node:
    node_id: str
    kind: str
    inputs: tuple[str, ...] = ()
    track: Track | None = None
    source: TrackSource | None = None
    curve: GainCurve | None = None
    start_frame: int = 0


class MixGraph:
    def __init__(self, sample_rate: int, total_frames: int) -> None:
        self.sample_rate = sample_rate
        self.total_frames = total_frames
        self._nodes: dict[str, Node] = {}
        self._order: list[str] | None = None

    def add_node(self, node: Node) -> None:
        if node.node_id in self._nodes:
            raise GraphError(f"duplicate node id {node.node_id!r}")
        self._nodes[node.node_id] = node
        self._order = None

    def validate(self) -> None:
        for node in self._nodes.values():
            for dep in node.inputs:
                if dep not in self._nodes:
                    raise GraphError(
                        f"node {node.node_id!r} depends on missing node {dep!r}"
                    )
        self._topo_order()

    def _topo_order(self) -> list[str]:
        if self._order is not None:
            return self._order
        indegree = {nid: 0 for nid in self._nodes}
        dependents: dict[str, list[str]] = {nid: [] for nid in self._nodes}
        for node in self._nodes.values():
            for dep in node.inputs:
                if dep not in self._nodes:
                    raise GraphError(
                        f"node {node.node_id!r} depends on missing node {dep!r}"
                    )
                indegree[node.node_id] += 1
                dependents[dep].append(node.node_id)
        ready = [nid for nid, deg in indegree.items() if deg == 0]
        heapq.heapify(ready)
        order: list[str] = []
        while ready:
            nid = heapq.heappop(ready)
            order.append(nid)
            for follower in dependents[nid]:
                indegree[follower] -= 1
                if indegree[follower] == 0:
                    heapq.heappush(ready, follower)
        if len(order) != len(self._nodes):
            remaining = sorted(n for n, d in indegree.items() if d > 0)
            raise GraphError(f"cycle detected among nodes: {remaining}")
        self._order = order
        return order

    def execute(self, start: int, count: int) -> dict[str, np.ndarray]:
        """Process one block, returning every node's output by node id."""
        outputs: dict[str, np.ndarray] = {}
        for nid in self._topo_order():
            node = self._nodes[nid]
            outputs[nid] = self._run_node(node, outputs, start, count)
        return outputs

    def _run_node(
        self, node: Node, outputs: dict[str, np.ndarray], start: int, count: int
    ) -> np.ndarray:
        if node.kind == "track":
            assert node.source is not None
            local_start = start - node.start_frame
            if local_start + count <= 0 or local_start >= self.total_frames:
                return np.zeros((count, node.source.channels), dtype=np.float64)
            read_start = max(local_start, 0)
            skip = read_start - local_start
            block = np.zeros((count, node.source.channels), dtype=np.float64)
            data = node.source.read(read_start, count - skip)
            block[skip : skip + len(data)] = data
            return block
        if node.kind == "adapt":
            block = outputs[node.inputs[0]]
            if block.shape[1] == 2:
                return block
            if block.shape[1] == 1:
                return np.repeat(block, 2, axis=1)
            raise AudioIOError(
                f"cannot adapt {block.shape[1]}-channel signal to stereo"
            )
        if node.kind == "gain":
            assert node.curve is not None and node.track is not None
            block = outputs[node.inputs[0]]
            gains = node.curve.gains_for_frames(
                start, count, self.sample_rate, track_offset=node.track.start
            )
            return block * gains[:, np.newaxis]
        if node.kind == "bus":
            acc = np.zeros((count, 2), dtype=np.float64)
            for dep in node.inputs:
                acc += outputs[dep]
            return acc
        if node.kind == "master":
            return outputs[node.inputs[0]]
        raise GraphError(f"unknown node kind {node.kind!r}")

    def release_before(self, frame: int) -> None:
        for node in self._nodes.values():
            if node.source is not None:
                node.source.release_before(frame - node.start_frame)


def build_graph(project: Project, sample_rate: int, total_frames: int) -> MixGraph:
    """Build the standard track→adapt→gain→bus→master graph."""
    graph = MixGraph(sample_rate, total_frames)
    gain_ids: list[str] = []
    for track in project.tracks:
        tid = track.track_id
        source = TrackSource(track.path, sample_rate)
        start_frame = int(round(track.start * sample_rate))
        graph.add_node(
            Node(f"track:{tid}", "track", track=track, source=source,
                 start_frame=start_frame)
        )
        graph.add_node(Node(f"adapt:{tid}", "adapt", inputs=(f"track:{tid}",)))
        graph.add_node(
            Node(
                f"gain:{tid}", "gain",
                inputs=(f"adapt:{tid}",),
                track=track,
                curve=GainCurve(track.keyframes),
            )
        )
        gain_ids.append(f"gain:{tid}")
    graph.add_node(Node("bus", "bus", inputs=tuple(sorted(gain_ids))))
    graph.add_node(Node("master", "master", inputs=("bus",)))
    graph.validate()
    return graph
