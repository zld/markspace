"""Level metrics: peak, RMS, frame count and clip count.

Clipping is defined as ``abs(sample) > 1.0``. RMS is accumulated as a sum of
squares across blocks so streaming and offline renders produce exactly the
same numbers regardless of where block boundaries fall.
"""

from __future__ import annotations

import math
from dataclasses import dataclass

import numpy as np

CLIP_THRESHOLD = 1.0


@dataclass
class Metrics:
    peak: float
    rms: float
    frames: int
    clips: int

    def as_dict(self) -> dict:
        return {
            "peak": self.peak,
            "rms": self.rms,
            "frames": self.frames,
            "clips": self.clips,
        }


class MetricsAccumulator:
    """Incrementally folds render blocks into one :class:`Metrics` result.

    All blocks passed to one accumulator must share the same channel count;
    empty input yields a zero-valued result.
    """

    def __init__(self) -> None:
        self.peak = 0.0
        # Per-block flat square sums; math.fsum at result time makes the RMS
        # independent of how blocks are partitioned (offline vs streaming).
        self._square_chunks: list[float] = []
        self.frames = 0
        self.clips = 0
        self.channels = 0

    def add(self, signal: np.ndarray) -> None:
        if signal.size == 0:
            return
        if signal.ndim == 1:
            signal = signal[:, np.newaxis]
        if self.channels == 0:
            self.channels = signal.shape[1]
        elif signal.shape[1] != self.channels:
            raise ValueError("all blocks must have the same channel count")
        self.peak = max(self.peak, float(np.max(np.abs(signal))))
        self._square_chunks.append(
            float(np.sum(signal * signal, dtype=np.float64))
        )
        self.frames += int(signal.shape[0])
        self.clips += int(np.count_nonzero(np.abs(signal) > CLIP_THRESHOLD))

    def result(self) -> Metrics:
        slots = self.frames * max(self.channels, 1)
        rms = math.sqrt(math.fsum(self._square_chunks) / slots) if slots else 0.0
        return Metrics(
            peak=self.peak,
            rms=rms,
            frames=self.frames,
            clips=self.clips,
        )


def compute(signal: np.ndarray) -> Metrics:
    """Compute metrics directly for a complete (frames x channels) buffer."""

    accumulator = MetricsAccumulator()
    accumulator.add(np.asarray(signal, dtype=np.float64))
    return accumulator.result()
