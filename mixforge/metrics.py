"""Streaming signal statistics: per-track peak/RMS and mix clip count."""

from __future__ import annotations

import numpy as np

CLIP_THRESHOLD = 1.0  # clip := |mix sample| > 1, counted per channel sample


class TrackMeter:
    """Accumulates peak and RMS over a track's bus contribution."""

    def __init__(self, name: str):
        self.name = name
        self.peak = 0.0
        self._sumsq = 0.0
        self._count = 0

    def update(self, block: np.ndarray) -> None:
        if block.size == 0:
            return
        self.peak = max(self.peak, float(np.abs(block).max()))
        self._sumsq += float(np.square(block, dtype=np.float64).sum())
        self._count += block.size

    @property
    def rms(self) -> float:
        return float(np.sqrt(self._sumsq / self._count)) if self._count else 0.0

    def as_dict(self) -> dict:
        return {"name": self.name, "peak": self.peak, "rms": self.rms}


class ClipMeter:
    """Counts clipped channel samples in the final mix (no limiting)."""

    def __init__(self, threshold: float = CLIP_THRESHOLD):
        self.threshold = threshold
        self.count = 0

    def update(self, mix: np.ndarray) -> None:
        if mix.size:
            self.count += int((np.abs(mix) > self.threshold).sum())
