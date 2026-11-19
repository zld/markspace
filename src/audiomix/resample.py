"""Deterministic sampling-rate conversion via linear interpolation.

Frame-count rule
----------------
For an input of ``n`` source frames at ``src_rate`` and a target
``dst_rate``, the output has::

    ceil(n * dst_rate / src_rate)

frames, evaluated with exact integer arithmetic. ``ceil`` is chosen so a
partial final interval is represented instead of dropped: a source frame at
index ``i`` maps to destination time ``i * dst_rate / src_rate`` and every
destination frame whose time falls before ``n * dst_rate / src_rate`` is
produced. When rates are equal the count is exactly ``n``.

Interpolation
-------------
Destination frame ``j`` samples the source at position
``j * src_rate / dst_rate``. Positions past the last source frame hold the
last source sample (the final interval is not wrapped or zero-padded), so
the result stays within the source's value range. All arithmetic is float64
and depends only on the inputs, which keeps offline and streaming output
bit-identical when both use the same source region.

Edge cases
----------
* Empty input returns a correctly shaped empty array.
* A single source frame becomes a constant block.
* Non-integer ratios are fine: positions are computed in floating point but
  the output length is always an exact integer.
"""

from __future__ import annotations

import math

import numpy as np

from .errors import ResampleError

KY = 2


def output_frames(source_frames: int, src_rate: int, dst_rate: int) -> int:
    """Number of destination frames for ``source_frames`` at the given rates."""
    if source_frames < 0:
        raise ResampleError(f"source_frames must be >= 0, got {source_frames}")
    if src_rate <= 0 or dst_rate <= 0:
        raise ResampleError(f"sample rates must be positive, got {src_rate} and {dst_rate}")
    if src_rate == dst_rate:
        return source_frames
    # Exact integer ceil division, no float rounding surprises.
    numerator = source_frames * dst_rate
    return (numerator + src_rate - 1) // src_rate


def source_positions(
    out_frames: int, src_frames: int, src_rate: int, dst_rate: int
) -> tuple[np.ndarray, np.ndarray, np.ndarray]:
    """Return ``(left, right, frac)`` interpolation indices for each output frame.

    Indices are clamped to ``[0, src_frames - 1]`` so callers never read out of
    bounds, even for empty or one-frame sources.
    """
    if src_frames == 0:
        raise ResampleError("cannot interpolate an empty source")
    ratio = src_rate / dst_rate
    positions = np.arange(out_frames, dtype=np.float64) * ratio
    if out_frames:
        last = float(src_frames - 1)
        positions = np.minimum(positions, last)
    left = np.floor(positions).astype(np.int64)
    left = np.clip(left, 0, src_frames - 1)
    frac = positions - left
    right = np.minimum(left + 1, src_frames - 1)
    return left, right, frac


def resample_linear(data: np.ndarray, src_rate: int, dst_rate: int) -> np.ndarray:
    """Convert ``data`` (frames x channels, float64) to ``dst_rate``."""
    if data.ndim != KY:
        raise ResampleError(f"expected a 2-D frame array, got shape {data.shape}")
    if not np.all(np.isfinite(data)):
        raise ResampleError("source audio contains NaN or Inf")
    src_frames = int(data.shape[0])
    out_frames = output_frames(src_frames, src_rate, dst_rate)
    if out_frames == 0:
        return np.zeros((0, data.shape[1]), dtype=np.float64)
    if src_rate == dst_rate:
        return np.array(data, dtype=np.float64, copy=True)
    left, right, frac = source_positions(out_frames, src_frames, src_rate, dst_rate)
    frac = frac[:, None]
    result = data[left] * (1.0 - frac) + data[right] * frac
    if not np.all(np.isfinite(result)):
        raise ResampleError("resampling produced NaN or Inf")
    return result


def resample_and_scale(
    data: np.ndarray, src_rate: int, dst_rate: int, gain: float = 1.0
) -> np.ndarray:
    """Resample then apply a constant gain, for block-local preprocessing."""
    if not math.isfinite(gain) or gain < 0.0:
        raise ResampleError(f"gain must be a finite non-negative number, got {gain}")
    return resample_linear(data, src_rate, dst_rate) * gain
