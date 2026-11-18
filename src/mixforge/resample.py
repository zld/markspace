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


def output_frame_count(in_frames: int, src_rate: int, dst_rate: int) -> int:
    """Frames produced for ``in_frames``; always at least one."""
    if in_frames <= 0:
        return 0
    return max(1, int(round(in_frames * dst_rate / src_rate)))


def source_window(local_start: int, local_end: int, ratio: float,
                  in_frames: int) -> tuple[int, int]:
    """Source frames needed to render output frames ``[local_start, local_end)``.

    The window includes the full kernel reach and is clamped to the file, so
    blocks never read outside the source.
    """
    if local_end <= local_start:
        return 0, 0
    first = int(np.floor(local_start * ratio)) - _HALF_TAPS + 1
    last = int(np.floor((local_end - 1) * ratio)) + _HALF_TAPS
    return max(0, first), min(in_frames, last + 1)


def resample_range(data: np.ndarray, data_start: int, src_rate: int, dst_rate: int,
                   out_start: int, out_end: int,
                   *, track_id: str | None = None) -> np.ndarray:
    """Render output frames ``[out_start, out_end)`` from a source window.

    ``data`` holds source frames starting at ``data_start``. Output frame
    positions are absolute, so any partition of a range yields bit-identical
    samples. Samples outside the window are treated as silence.
    """
    block = np.asarray(data, dtype=np.float64)
    if block.ndim != 2:
        raise RenderError("resampler expects a 2-D (frames, channels) array",
                          track_id=track_id, field="audio")
    if src_rate <= 0 or dst_rate <= 0:
        raise RenderError("sample rates must be positive", track_id=track_id,
                          field="rate")
    count = out_end - out_start
    if count <= 0:
        return np.zeros((0, block.shape[1]), dtype=np.float64)
    if src_rate == dst_rate:
        local = np.arange(out_start, out_end) - data_start
        valid = (local >= 0) & (local < block.shape[0])
        out = np.zeros((count, block.shape[1]), dtype=np.float64)
        out[valid] = block[local[valid]]
        return out

    ratio = src_rate / dst_rate
    positions = np.arange(out_start, out_end, dtype=np.float64) * ratio
    centres = np.floor(positions).astype(np.int64)
    phases = positions - centres

    offsets = np.arange(-_HALF_TAPS + 1, _HALF_TAPS + 1, dtype=np.int64)
    indices = centres[:, None] + offsets[None, :] - data_start
    valid = (indices >= 0) & (indices < block.shape[0])
    clamped = np.clip(indices, 0, block.shape[0] - 1)
    weights = _kaiser_sinc(phases[:, None] - offsets[None, :])
    weights = np.where(valid, weights, 0.0)
    return np.stack(
        [np.sum(weights * block[clamped, ch], axis=1)
         for ch in range(block.shape[1])],
        axis=1,
    )


def resample(samples: np.ndarray, src_rate: int, dst_rate: int,
             *, track_id: str | None = None, chunk: int = 4096) -> np.ndarray:
    """Resample a whole (frames, channels) array in bounded-memory chunks."""
    data = np.asarray(samples, dtype=np.float64)
    if data.ndim != 2:
        raise RenderError("resampler expects a 2-D (frames, channels) array",
                          track_id=track_id, field="audio")
    if src_rate <= 0 or dst_rate <= 0:
        raise RenderError("sample rates must be positive", track_id=track_id,
                          field="rate")
    total = output_frame_count(data.shape[0], src_rate, dst_rate)
    out = np.zeros((total, data.shape[1]), dtype=np.float64)
    for start in range(0, total, max(1, chunk)):
        end = min(total, start + max(1, chunk))
        out[start:end] = resample_range(data, 0, src_rate, dst_rate, start, end,
                                        track_id=track_id)
    return out


def default_matrix(channel_count: int) -> tuple[tuple[float, ...], ...]:
    """Identity layout up to stereo; mono is duplicated to both outputs."""
    if channel_count == 1:
        return ((1.0,), (1.0,))
    if channel_count == 2:
        return ((1.0, 0.0), (0.0, 1.0))
    raise RenderError(
        f"source has {channel_count} channels; provide an explicit 2-row channel "
        f"matrix",
        field="channels",
    )


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


def apply_matrix(samples: np.ndarray, matrix: np.ndarray) -> np.ndarray:
    """Project (frames, source channels) to stereo with a validated matrix.

    Written as scalar multiply-adds rather than a BLAS matmul so the result
    does not depend on array length or block size.
    """
    data = np.asarray(samples, dtype=np.float64)
    weights = np.asarray(matrix, dtype=np.float64)
    out = np.zeros((data.shape[0], weights.shape[0]), dtype=np.float64)
    for row in range(weights.shape[0]):
        acc = np.zeros(data.shape[0], dtype=np.float64)
        for col in range(data.shape[1]):
            acc += data[:, col] * float(weights[row, col])
        out[:, row] = acc
    return out


def apply_layout(samples: np.ndarray, matrix: tuple[tuple[float, ...], ...],
                 *, track_id: str) -> np.ndarray:
    """Validate an explicit layout against the source width, then apply it."""
    data = np.asarray(samples, dtype=np.float64)
    if data.ndim != 2:
        raise RenderError("channel layout expects a 2-D (frames, channels) array",
                          track_id=track_id, field="audio")
    validate_layout(data.shape[1], matrix, track_id=track_id)
    return apply_matrix(data, np.array(matrix, dtype=np.float64))


def db_to_gain(db: float) -> float:
    return 10.0 ** (db / 20.0)


def ms_to_frames(ms: float, rate: int) -> int:
    """Convert milliseconds to frames with deterministic half-up rounding."""
    return int(math.floor(ms * rate / 1000.0 + 0.5))
