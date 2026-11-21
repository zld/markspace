"""Deterministic sample-rate conversion with a windowed-sinc kernel.

The same pure-NumPy routine is used by both render paths, so offline and
stream outputs stay bit-identical.  Edge samples use weight normalization,
which gives a defined result for single-sample inputs and short tails.
"""

from __future__ import annotations

import math

import numpy as np

# Kernel half-width measured on the output sample grid.
HALF_WIDTH = 16


def output_frame_count(src_frames: int, src_rate: int, dst_rate: int) -> int:
    if src_frames <= 0:
        return 0
    return int(round(src_frames * dst_rate / src_rate))


def _weights_for_center(center: float, ratio: float) -> tuple[np.ndarray, np.ndarray]:
    """Return (input-sample offsets, normalized kernel weights) for one output."""
    cutoff = min(1.0, ratio)
    half = HALF_WIDTH / cutoff
    k_min = int(math.ceil(center - half))
    k_max = int(math.floor(center + half))
    offsets = np.arange(k_min, k_max + 1, dtype=np.float64)
    distance = center - offsets
    # Hann window expressed relative to the (cutoff-scaled) half width.
    window = 0.5 + 0.5 * np.cos(np.pi * distance / half)
    # No explicit cutoff gain: per-center weight normalization guarantees
    # unity DC gain while the stretched kernel still suppresses images.
    weights = np.sinc(cutoff * distance) * window
    total = weights.sum()
    if total != 0.0:
        weights = weights / total
    return offsets.astype(np.int64), weights


def resample(signal: np.ndarray, src_rate: int, dst_rate: int) -> np.ndarray:
    """Resample ``signal`` of shape (frames, channels).

    Equal rates return a copy, so callers may freely mutate the result.
    Empty input yields empty output with the same channel count.
    """
    if signal.ndim != 2:
        raise ValueError("resample expects a 2-D (frames, channels) array")
    if src_rate == dst_rate:
        return signal.copy()
    src_frames, channels = signal.shape
    out_frames = output_frame_count(src_frames, src_rate, dst_rate)
    if out_frames == 0:
        return np.zeros((0, channels), dtype=np.float64)
    ratio = dst_rate / src_rate
    output = np.zeros((out_frames, channels), dtype=np.float64)
    for i in range(out_frames):
        center = i / ratio
        indices, weights = _weights_for_center(center, ratio)
        valid = (indices >= 0) & (indices < src_frames)
        if not valid.any():
            continue
        indices = indices[valid]
        weights = weights[valid]
        output[i] = signal[indices].T @ weights
    return output
