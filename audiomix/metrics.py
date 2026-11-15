"""Numeric statistics for rendered tracks and mixes.

A :class:`MetricsAccumulator` consumes blocks incrementally so neither the
offline nor the streaming path has to retain the whole signal to compute
statistics.  Peak is the maximum absolute sample; RMS is the energy mean over
every channel-sample; clip counting distinguishes per-channel clipping.
"""

from __future__ import annotations

import numpy as np


class MetricsAccumulator:
    def __init__(self) -> None:
        self.peak = 0.0
        self._sum_sq = 0.0
        self._count = 0
        self.clip_samples = 0

    def add_block(self, block: np.ndarray) -> None:
        if block.size == 0:
            return
        self.peak = max(self.peak, float(np.max(np.abs(block))))
        self._sum_sq += float(np.sum(block * block, dtype=np.float64))
        self._count += int(block.size)
        self.clip_samples += int(np.count_nonzero(np.abs(block) > 1.0))

    @property
    def rms(self) -> float:
        if self._count == 0:
            return 0.0
        return float(np.sqrt(self._sum_sq / self._count))

    def as_dict(self) -> dict:
        return {
            "peak": self.peak,
            "rms": self.rms,
            "clip_samples": self.clip_samples,
        }


def automation_samples(curve, frames: tuple[int, ...]) -> dict[int, float]:
    """Sample the gain curve at the requested absolute project frames."""
    return {int(frame): float(curve.sample(int(frame))) for frame in frames}


def summarize_track(
    track_id: str,
    accumulator: MetricsAccumulator,
    curve,
    sample_frames: tuple[int, ...],
) -> dict:
    """Build the per-track report entry with stats and curve samples."""
    entry = {"id": track_id, **accumulator.as_dict()}
    entry["automation_samples"] = automation_samples(curve, sample_frames)
    return entry
