"""Deterministic linear-interpolation sample-rate conversion."""

from __future__ import annotations

import numpy as np

from .errors import ConfigError, ProcessingError


def output_length(input_frames: int, source_rate: int, target_rate: int) -> int:
    """Target duration rounded to whole frames (half away from zero)."""

    if input_frames < 0:
        raise ConfigError("input_frames must be non-negative", location="resample")
    if source_rate <= 0 or target_rate <= 0:
        raise ConfigError("sample rates must be positive", location="resample")
    return int(np.floor(input_frames * (target_rate / source_rate) + 0.5))


def interpolation_indices(
    positions: np.ndarray, total_frames: int, *, location: str = "resample"
) -> tuple[np.ndarray, np.ndarray, np.ndarray]:
    """Neighbours and fraction for linear interpolation at ``positions``.

    Shared by whole-buffer resampling and block-local graph reads so both
    paths use bit-identical arithmetic.
    """

    pos = np.asarray(positions, dtype=np.float64)
    if pos.ndim != 1:
        raise ProcessingError("interpolation positions must be 1-D", location=location)
    if pos.size == 0:
        return (
            np.empty(0, dtype=np.int64),
            np.empty(0, dtype=np.int64),
            np.empty(0, dtype=np.float64),
        )
    if not np.all(np.isfinite(pos)):
        raise ProcessingError("interpolation positions must be finite", location=location)
    if total_frames <= 0:
        raise ProcessingError("cannot interpolate an empty source", location=location)
    if np.any(pos < 0) or np.any(pos > total_frames - 1 + 1e-9):
        raise ProcessingError(
            "interpolation position is outside the source",
            location=location,
            context={"max_position": float(pos.max()), "source_frames": total_frames},
        )
    left = np.floor(pos).astype(np.int64)
    np.clip(left, 0, total_frames - 1, out=left)
    right = np.minimum(left + 1, total_frames - 1)
    fraction = np.clip(pos - left, 0.0, 1.0)
    return left, right, fraction


def blend_linear(
    data: np.ndarray, left: np.ndarray, right: np.ndarray, fraction: np.ndarray
) -> np.ndarray:
    """``data[left] * (1 - f) + data[right] * f`` for a 2-D buffer."""

    weight = fraction[:, None]
    return data[left] * (1.0 - weight) + data[right] * weight


def resample_audio(
    data: np.ndarray,
    source_rate: int,
    target_rate: int,
    *,
    location: str = "audio",
) -> np.ndarray:
    """Convert ``(frames, channels)`` float audio to ``target_rate``.

    Positions are ``i * source_rate / target_rate``; the final index is
    clamped so upsampling never reads past the source.  Empty input, a
    single source frame, and a zero-length target are all explicit cases.
    """

    array = np.asarray(data)
    if array.ndim != 2:
        raise ProcessingError(
            f"expected a 2-D (frames, channels) array, got {array.ndim}-D", location=location
        )
    if not np.issubdtype(array.dtype, np.number):
        raise ProcessingError("audio samples must be numeric", location=location)
    if array.size and not np.all(np.isfinite(array)):
        raise ProcessingError("audio contains NaN or infinite samples", location=location)
    if source_rate <= 0 or target_rate <= 0:
        raise ConfigError("sample rates must be positive", location="resample")
    if array.dtype != np.float64:
        array = array.astype(np.float64)

    source_frames, channels = array.shape
    target_frames = output_length(source_frames, source_rate, target_rate)
    if target_frames == 0 or source_frames == 0:
        return np.empty((0, channels), dtype=np.float64)
    if source_frames == 1 or source_rate == target_rate:
        if source_rate == target_rate:
            return array.copy()
        return np.repeat(array[:1], target_frames, axis=0)

    positions = np.arange(target_frames, dtype=np.float64) * (source_rate / target_rate)
    positions = np.minimum(positions, float(source_frames - 1))
    left, right, fraction = interpolation_indices(positions, source_frames, location=location)
    output = blend_linear(array, left, right, fraction)
    if not np.all(np.isfinite(output)):
        raise ProcessingError("resampling produced non-finite samples", location=location)
    return output
