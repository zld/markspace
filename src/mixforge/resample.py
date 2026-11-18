"""Sample-rate conversion and channel-layout normalisation.

Resampling is deterministic band-limited interpolation: a Kaiser-windowed
sinc kernel with a fixed table, evaluated at exact fractional positions.
The same input, rates and duration always produce bit-identical output.
"""

from __future__ import annotations

import math

import numpy as np

from .errors import RenderError

# 64 zero crossings each side; enough attenuation for 48 kHz deliverables.
_HALF_TAPS = 64
_BETA = 8.6
_CUTOFF = 0.90  # of the lower Nyquist rate, leaves a transition band


def _kaiser_sinc(phase: np.ndarray) -> np.ndarray:
    """Windowed-sinc kernel sampled at ``phase`` in [-HALF_TAPS, HALF_TAPS]."""
    t = phase
    window = np.i0(_BETA * np.sqrt(np.clip(1.0 - (t / _HALF_TAPS) ** 2, 0.0, 1.0)))
    window /= np.i0(_BETA)
    x = np.pi * _CUTOFF * t
    kernel = np.where(np.abs(x) < 1e-12, 1.0, np.sin(x) / np.where(x == 0, 1.0, x))
    return _CUTOFF * kernel * window


def resample(samples: np.ndarray, src_rate: int, dst_rate: int,
             *, track_id: str | None = None) -> np.ndarray:
    """Resample ``samples`` (frames, channels) with deterministic sinc interpolation."""
    data = np.asarray(samples, dtype=np.float64)
    if data.ndim != 2:
        raise RenderError("resampler expects a 2-D (frames, channels) array",
                          track_id=track_id, field="audio")
    if src_rate <= 0 or dst_rate <= 0:
        raise RenderError("sample rates must be positive", track_id=track_id,
                          field="rate")
    if src_rate == dst_rate or data.shape[0] == 0:
        return data.copy()

    in_frames = data.shape[0]
    out_frames = max(1, int(round(in_frames * dst_rate / src_rate)))
    ratio = src_rate / dst_rate
    # Positions in source frames for each output frame.
    positions = np.arange(out_frames, dtype=np.float64) * ratio
    centres = np.floor(positions).astype(np.int64)
    phases = positions - centres

    offsets = np.arange(-_HALF_TAPS + 1, _HALF_TAPS + 1, dtype=np.int64)
    indices = centres[:, None] + offsets[None, :]
    # Outside the source the signal is zero (no wrap, no reflection).
    valid = (indices >= 0) & (indices < in_frames)
    clamped = np.clip(indices, 0, in_frames - 1)
    weights = _kaiser_sinc(phases[:, None] - offsets[None, :])
    weights = np.where(valid, weights, 0.0)

    out = np.zeros((out_frames, data.shape[1]), dtype=np.float64)
    for channel in range(data.shape[1]):
        out[:, channel] = np.sum(weights * data[clamped, channel], axis=1)
    return out


def validate_layout(channel_count: int, matrix: tuple[tuple[float, ...], ...],
                    *, track_id: str) -> None:
    """Reject a channel matrix whose width does not match the source.

    Silent channel drops are never allowed: the manifest must describe every
    source channel explicitly.
    """
    if len(matrix) != 2:
        raise RenderError("channel matrix must map to 2 output channels",
                          track_id=track_id, field="channels")
    width = len(matrix[0]) if matrix else 0
    if channel_count != width:
        raise RenderError(
            f"channel matrix has {width} input column(s) but source has "
            f"{channel_count} channel(s)",
            track_id=track_id, field="channels",
        )


def apply_layout(samples: np.ndarray, matrix: tuple[tuple[float, ...], ...],
                 *, track_id: str) -> np.ndarray:
    """Project (frames, source channels) to stereo using the explicit matrix."""
    data = np.asarray(samples, dtype=np.float64)
    if data.ndim != 2:
        raise RenderError("channel layout expects a 2-D (frames, channels) array",
                          track_id=track_id, field="audio")
    validate_layout(data.shape[1], matrix, track_id=track_id)
    weights = np.array(matrix, dtype=np.float64)  # (2, src)
    return data @ weights.T


def db_to_gain(db: float) -> float:
    return 10.0 ** (db / 20.0)


def ms_to_frames(ms: float, rate: int) -> int:
    """Convert milliseconds to frames with deterministic half-up rounding."""
    return int(math.floor(ms * rate / 1000.0 + 0.5))
