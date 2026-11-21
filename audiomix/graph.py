"""Shared mix graph: routing validation and block-based mixdown engine.

Both the offline and streaming drivers call :meth:`MixEngine.process_block`;
the only difference is how the timeline is chunked.  Track contributions
are rendered per block from stateful resamplers and gain curves, summed
into buses in track order, then propagated along the bus DAG in a fixed
topological order, so results do not depend on the chunking.
"""

from __future__ import annotations

import numpy as np

from .automation import curve_for_track
from .io import map_channels, read_wav
from .model import Manifest
from .resample import Resampler


class GraphError(Exception):
    """Raised for missing bus nodes and routing cycles."""


def validate_routing(buses: dict) -> list[str]:
    """Validate the bus DAG; return bus names in propagation order.

    Every bus with a null target feeds ``master``.  Raises GraphError with
    the offending path for unknown targets and cycles.
    """
    for name, spec in buses.items():
        target = spec.target
        if target is not None and target not in buses:
            raise GraphError(f"bus {name!r} routes to missing bus {target!r}")

    def parent(name: str) -> str | None:
        if name == "master":
            return None
        target = buses[name].target
        return "master" if target is None else target

    order: list[str] = []
    state: dict[str, int] = {}  # 0=unvisited 1=in-stack 2=done

    def visit(node: str, stack: list[str]) -> None:
        state[node] = 1
        stack.append(node)
        p = parent(node)
        if p is not None:
            if state.get(p, 0) == 1:
                cycle = stack[stack.index(p):] + [p]
                raise GraphError("routing cycle detected: " + " -> ".join(cycle))
            if state.get(p, 0) == 0:
                visit(p, stack)
        stack.pop()
        state[node] = 2
        order.append(node)

    for name in buses:
        if state.get(name, 0) == 0:
            visit(name, [])
    order.reverse()
    return order  # children always appear before their parents


class TrackVoice:
    """One manifest track bound to the output rate/channel layout."""

    def __init__(self, spec, rate: int, channels: int):
        source, src_rate = read_wav(spec.file, spec.index)
        mapped = map_channels(source, channels)
        self.index = spec.index
        self.bus = spec.bus
        self.resampler = Resampler(mapped, src_rate, rate)
        self.curve = curve_for_track(spec)
        self.start_frame = int(round(spec.offset * rate))

    @property
    def end_frame(self) -> int:
        return self.start_frame + self.resampler.total_frames

    def reset(self) -> None:
        self.resampler.reset()

    def render(self, g0: int, g1: int, rate: int):
        """Render the overlap with global range [g0, g1).

        Returns (block, offset_in_block) or None.  Blocks are consumed
        sequentially, so the resampler cursor always matches the requested
        local position.
        """
        t0 = max(g0, self.start_frame)
        t1 = min(g1, self.end_frame)
        if t0 >= t1:
            return None
        block = self.resampler.read(t1 - t0)
        block = self.curve.apply(block, t0, rate)
        return block, t0 - g0


class MixEngine:
    """Renders a manifest through the bus graph, block by block."""

    def __init__(self, manifest: Manifest, rate: int, channels: int):
        self.rate = rate
        self.channels = channels
        self._buses = manifest.buses
        self._order = validate_routing(self._buses)
        self.voices = [TrackVoice(spec, rate, channels) for spec in manifest.tracks]
        self.total_frames = max(v.end_frame for v in self.voices)
        self.reset()

    def reset(self) -> None:
        for v in self.voices:
            v.reset()
        # Post-automation per-track accumulators for the metrics report.
        self._peak = [0.0] * len(self.voices)
        self._sumsq = [0.0] * len(self.voices)
        self._count = [0] * len(self.voices)

    def track_stats(self) -> list[dict]:
        stats = []
        for i, v in enumerate(self.voices):
            n = self._count[i]
            rms = float(np.sqrt(self._sumsq[i] / n)) if n else 0.0
            stats.append({"index": v.index, "bus": v.bus,
                          "peak": self._peak[i], "rms": rms})
        return stats

    def process_block(self, g0: int, num_frames: int) -> np.ndarray:
        """Mix global frames [g0, g0+num_frames) into a master block."""
        g1 = g0 + num_frames
        bus_blocks = {
            name: np.zeros((num_frames, self.channels), dtype=np.float64)
            for name in self._buses
        }
        for i, voice in enumerate(self.voices):
            out = voice.render(g0, g1, self.rate)
            if out is None:
                continue
            block, off = out
            bus_blocks[voice.bus][off : off + block.shape[0]] += block
            self._peak[i] = max(self._peak[i], float(np.abs(block).max()))
            self._sumsq[i] += float(np.square(block).sum())
            self._count[i] += block.size
        for name in self._order:
            if name == "master":
                continue
            target = self._buses[name].target or "master"
            bus_blocks[target] += bus_blocks[name]
        return bus_blocks["master"]
