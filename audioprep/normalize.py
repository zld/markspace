"""Loudness-based gain decision with a sample-peak ceiling.

The gain is decided in two stages:

1. **loudness gain** -- the dB shift that moves the measured integrated
   loudness onto the requested target LUFS;
2. **peak constraint** -- if applying that gain would push any sample beyond
   the peak ceiling (given in dBFS), the gain is reduced so the largest
   absolute sample lands exactly on the ceiling.

Silence or unmeasurable loudness yields no gain (1.0). A gain that was
reduced by the peak stage is reported as ceiling-constrained, so the report
can explain why the target LUFS was not reached. Clipping is counted in
absolute-value terms against the mathematical full-scale level 1.0, both
before and after normalization.
"""

from __future__ import annotations

import math
from dataclasses import dataclass

import numpy as np

FULL_SCALE = 1.0


@dataclass(frozen=True)
class GainDecision:
    """Final linear gain and which stage bounded it."""

    linear_gain: float
    target_linear_gain: float
    peak_constrained: bool


def db_to_linear(db: float) -> float:
    return 10.0 ** (db / 20.0)


def linear_to_db(linear: float) -> float:
    return 20.0 * math.log10(linear)


def decide_gain(
    measured_lufs: float | None,
    target_lufs: float,
    peak_ceiling_dbfs: float,
    peak_abs: float,
) -> GainDecision:
    """Compute the gain honoring loudness target and peak ceiling."""
    if measured_lufs is None or not math.isfinite(measured_lufs):
        return GainDecision(1.0, 1.0, False)

    target_gain = db_to_linear(target_lufs - measured_lufs)
    ceiling_linear = db_to_linear(peak_ceiling_dbfs)

    if peak_abs <= 0.0:
        return GainDecision(target_gain, target_gain, False)

    # Largest gain allowed by the sample-peak ceiling.
    peak_gain = ceiling_linear / peak_abs
    if target_gain <= peak_gain:
        return GainDecision(target_gain, target_gain, False)
    return GainDecision(peak_gain, target_gain, True)


def count_clipped(signal: np.ndarray) -> int:
    """Count samples whose absolute value reaches mathematical full scale."""
    signal = np.asarray(signal, dtype=np.float64)
    if signal.size == 0:
        return 0
    return int(np.count_nonzero(np.abs(signal) >= FULL_SCALE))


def apply_gain(signal: np.ndarray, linear_gain: float) -> np.ndarray:
    """Apply a scalar gain; the ceiling keeps results within [-1, 1]."""
    return np.asarray(signal, dtype=np.float64) * linear_gain


def peak_abs(signal: np.ndarray) -> float:
    """Maximum absolute sample, or 0.0 for an empty/silent signal."""
    signal = np.asarray(signal, dtype=np.float64)
    if signal.size == 0:
        return 0.0
    return float(np.max(np.abs(signal)))


class StreamingStats:
    """Accumulate RMS energy, peak and clipping counts frame-block by block."""

    def __init__(self) -> None:
        self._square_sum = 0.0
        self._sample_count = 0
        self._peak = 0.0
        self._clipped = 0

    def add_block(self, block: np.ndarray) -> None:
        block = np.asarray(block, dtype=np.float64)
        if not np.all(np.isfinite(block)):
            raise FloatingPointError("non-finite sample in stats input")
        self._square_sum += float(np.sum(np.square(block)))
        self._sample_count += block.size
        self._peak = max(self._peak, float(np.max(np.abs(block))) if block.size else 0.0)
        self._clipped += count_clipped(block)

    @property
    def peak(self) -> float:
        return self._peak

    @property
    def clipped(self) -> int:
        return self._clipped

    def rms_dbfs(self) -> float | None:
        if self._sample_count == 0 or self._square_sum <= 0.0:
            return None
        energy = self._square_sum / self._sample_count
        return 20.0 * math.log10(math.sqrt(energy))
