"""Sample-rate conversion and channel-layout normalization.

The resampler is a deterministic streaming linear interpolator.  Source
positions are derived from the *global* output frame index with integer
(rational) arithmetic, so feeding one large buffer or many small blocks
produces bit-identical samples; only the carry frame (one sample) is kept
between blocks, hence memory does not grow with stream length.
"""
from __future__ import annotations

import numpy as np

from .model import RenderError


def ceil_div(a: int, b: int) -> int:
    return -((-a) // b)


def resampled_frame_count(src_frames: int, src_rate: int,
                          dst_rate: int) -> int:
    """Number of output frames produced from ``src_frames`` input frames.

    The final source frame is held (clamped) to cover the trailing segment,
    so an N-frame source always yields ``ceil(N * dst / src)`` frames.
    """
    if src_frames <= 0:
        return 0
    return ceil_div(src_frames * int(dst_rate), int(src_rate))


class LinearResampler:
    """Stateful streaming linear resampler (float64 internally)."""

    def __init__(self, src_rate: int, dst_rate: int):
        if src_rate <= 0 or dst_rate <= 0:
            raise ValueError("sample rates must be positive")
        self.src_rate = int(src_rate)
        self.dst_rate = int(dst_rate)
        self._in_pos = 0          # global index one past consumed frames
        self._out_index = 0       # global index of next output frame
        self._carry: np.ndarray | None = None  # last consumed frame (ch,)

    @property
    def frames_produced(self) -> int:
        return self._out_index

    def process(self, frames: np.ndarray) -> np.ndarray:
        """Consume ``frames`` of shape (n, ch) and return resampled frames."""
        if frames.ndim == 1:
            frames = frames[:, np.newaxis]
        if frames.shape[0] == 0 and self._carry is None:
            return np.empty((0, frames.shape[1]), dtype=np.float64)
        if self._carry is not None:
            buf = np.vstack((self._carry[np.newaxis, :], frames))
        else:
            buf = frames.astype(np.float64, copy=False)
        length = buf.shape[0]
        start_index = self._in_pos - (1 if self._carry is not None else 0)
        # Output n needs frames at floor(p_n) and floor(p_n)+1, both inside
        # the current buffer: p_n < start_index + length - 1.
        span_end = start_index + length - 1
        n_max = ceil_div(span_end * self.dst_rate, self.src_rate) - 1
        count = max(0, n_max + 1 - self._out_index)
        if count > 0:
            globals_ = np.arange(self._out_index,
                                 self._out_index + count, dtype=np.int64)
            scaled = globals_ * np.int64(self.src_rate)
            i0_global = scaled // self.dst_rate
            numerator = scaled - i0_global * self.dst_rate
            frac = (numerator / self.dst_rate)[:, np.newaxis]
            local = (i0_global - start_index).astype(np.int64)
            left = buf[local]
            right = buf[local + 1]
            out = left + frac * (right - left)
        else:
            out = np.empty((0, buf.shape[1]), dtype=np.float64)
        if length > 0:
            self._carry = buf[-1].copy()
        self._in_pos = start_index + length
        self._out_index += count
        return out

    def flush(self) -> np.ndarray:
        """Emit tail samples by holding the final source frame."""
        if self._carry is None or self._in_pos <= 0:
            return np.zeros((0, 0), dtype=np.float64)
        limit = ceil_div(self._in_pos * self.dst_rate, self.src_rate)
        count = max(0, limit - self._out_index)
        out = np.repeat(self._carry[np.newaxis, :], count, axis=0)
        self._out_index += count
        return out


def map_channels(data: np.ndarray, channel_map: tuple[int, ...] | None,
                 out_channels: int) -> np.ndarray:
    """Normalize ``data`` (n, src_ch) to ``out_channels`` columns.

    An explicit *channel_map* gives the source index for each output channel
    and is bounds-checked against the actual file layout.  Without a map the
    layout is widened/narrowed deterministically (mono duplicates; wider
    sources take the first channels; narrower sources repeat the last).
    """
    if data.ndim == 1:
        data = data[:, np.newaxis]
    n, src_ch = data.shape
    if channel_map is not None:
        if len(channel_map) != out_channels:
            raise RenderError(
                f"channel_map length {len(channel_map)} does not match "
                f"output channel count {out_channels}")
        if max(channel_map) >= src_ch:
            raise RenderError(
                f"channel_map references channel {max(channel_map)} but "
                f"source only has {src_ch} channel(s)")
        return data[:, np.asarray(channel_map, dtype=np.int64)]
    if src_ch == out_channels:
        return data
    out = np.empty((n, out_channels), dtype=np.float64)
    if src_ch == 1:
        out[:] = data[:, :1]
    elif src_ch >= out_channels:
        out[:] = data[:, :out_channels]
    else:
        out[:, :src_ch] = data
        out[:, src_ch:] = data[:, -1:]
    return out


def frame_offset(start_sec: float, rate: int) -> int:
    """Round a start time in seconds to a whole output frame offset."""
    if start_sec < 0.0:
        raise RenderError(f"negative start time {start_sec}")
    return int(round(float(start_sec) * rate))
