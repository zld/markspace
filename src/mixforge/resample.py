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
    left = np.floor(positions).astype(np.int64)
    np.clip(left, 0, source_frames - 1, out=left)
    right = np.minimum(left + 1, source_frames - 1)
    fraction = positions - left
    fraction = np.clip(fraction, 0.0, 1.0)[:, None]
    output = array[left] * (1.0 - fraction) + array[right] * fraction
    if not np.all(np.isfinite(output)):
        raise ProcessingError("resampling produced non-finite samples", location=location)
    return output
