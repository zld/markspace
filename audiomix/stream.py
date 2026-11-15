"""Streaming renderer: chunked, stateful consumption of the same mix graph.

The renderer wraps a :class:`~audiomix.graph.MixGraph` and exposes the project
as an iterator of fixed-size blocks (the last block may be shorter).  Block
sizes may even vary between calls: graph state is fully self-contained, so the
produced sample sequence is independent of how the stream was chunked.  A
helper compares two stream renders and reports the maximum absolute parity
error against the offline path.
"""

from __future__ import annotations

import numpy as np

from .graph import CHANNELS, MixGraph
from .metrics import MetricsAccumulator, summarize_track


class StreamRenderer:
    def __init__(self, project, block_frames: int):
        if not 1 <= block_frames <= 8192:
            raise ValueError("block_frames must be within [1, 8192]")
        self.block_frames = block_frames
        self.graph = MixGraph(project)
        self._mix_metrics = MetricsAccumulator()
        self._frames_emitted = 0

    def __enter__(self) -> "StreamRenderer":
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        self.close()

    def read_block(self) -> np.ndarray:
        """Return the next block; an empty array signals end of stream."""
        block = self.graph.read_block(self.block_frames)
        if block.shape[0]:
            self._mix_metrics.add_block(block)
            self._frames_emitted += block.shape[0]
        return block

    def __iter__(self):
        while True:
            block = self.read_block()
            if block.shape[0] == 0:
                return
            yield block

    def render_to_array(self) -> np.ndarray:
        chunks = list(self)
        if not chunks:
            return np.empty((0, CHANNELS), dtype=np.float64)
        return np.concatenate(chunks, axis=0)

    @property
    def position(self) -> int:
        return self.graph.position

    def report(self, sample_frames: tuple[int, ...] = ()) -> dict:
        return {
            "frames": self._frames_emitted,
            "clip_samples": self._mix_metrics.clip_samples,
            "mix_peak": self._mix_metrics.peak,
            "mix_rms": self._mix_metrics.rms,
            "tracks": [
                summarize_track(
                    node.track_id, node.accumulator, node.curve, sample_frames
                )
                for node in self.graph.nodes
            ],
        }

    def close(self) -> None:
        self.graph.close()


def render_stream(project, block_frames: int) -> tuple[np.ndarray, dict]:
    """Render the whole project in memory via the streaming path."""
    with StreamRenderer(project, block_frames) as renderer:
        data = renderer.render_to_array()
        report = renderer.report(project.automation_sample_frames)
    return data, report


def parity_error(reference: np.ndarray, candidate: np.ndarray) -> float:
    """Maximum absolute sample difference between two renders."""
    if reference.shape != candidate.shape:
        raise ValueError(
            f"shape mismatch in parity check: {reference.shape} vs {candidate.shape}"
        )
    if reference.size == 0:
        return 0.0
    return float(np.max(np.abs(reference - candidate)))
