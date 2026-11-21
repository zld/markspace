"""Streaming level statistics: peak, RMS and threshold clip counting.

Accumulators consume fixed-size blocks including zero-padded and short tail
blocks, so statistics cover the whole timeline including silence.
"""

from __future__ import annotations

import math

import numpy as np

NEG_INF_DB = -1200.0


def to_dbfs(value: float) -> float:
    if value <= 0.0:
        return NEG_INF_DB
    return 20.0 * math.log10(value)


class LevelStats:
    """Online peak/RMS/clip accumulator for a 2-D (frames, channels) signal."""

    def __init__(self, threshold: float = 1.0) -> None:
        self.threshold = threshold
        self.peak = 0.0
        self._sum_squares = 0.0
        self._sample_count = 0
        self.clip_count = 0

    def add(self, block: np.ndarray) -> None:
        if block.size == 0:
            return
        self.peak = max(self.peak, float(np.max(np.abs(block))))
        self._sum_squares += float(np.sum(block * block, dtype=np.float64))
        # Individual scalar samples over threshold count as clips.
        self.clip_count += int(np.count_nonzero(np.abs(block) > self.threshold))
        self._sample_count += block.size

    @property
    def rms(self) -> float:
        if self._sample_count == 0:
            return 0.0
        return math.sqrt(self._sum_squares / self._sample_count)

    def to_dict(self) -> dict:
        return {
            "peak_dbfs": round(to_dbfs(self.peak), 6),
            "rms_dbfs": round(to_dbfs(self.rms), 6),
            "clip_count": self.clip_count,
        }
