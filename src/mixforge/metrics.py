"""Sample statistics accumulators.

Statistics are accumulated block by block so offline and streaming
renders produce identical reports. Clipping is *counted*, never
corrected: a clip sample is any sample whose absolute value exceeds 1.0,
and no limiting is applied to the signal itself.
"""

from __future__ import annotations

import math

import numpy as np


class StatsAccumulator:
    """Running peak / RMS / clip-count over a sequence of blocks."""

    def __init__(self) -> None:
        self._peak = 0.0
        self._sum_sq = 0.0
        self._count = 0
        self._clips = 0

    def update(self, block: np.ndarray) -> None:
        block = np.asarray(block, dtype=np.float64)
        if block.size == 0:
            return
        self._peak = max(self._peak, float(np.abs(block).max()))
        self._sum_sq += float(np.square(block).sum())
        self._count += block.size
        self._clips += int((np.abs(block) > 1.0).sum())

    def report(self) -> dict:
        rms = math.sqrt(self._sum_sq / self._count) if self._count else 0.0
        return {
            "peak": self._peak,
            "rms": rms,
            "clip_samples": self._clips,
        }
