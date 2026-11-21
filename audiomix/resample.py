"""Sample-rate conversion with a 64-tap windowed-sinc interpolator.

The resampler is pull-based: the source buffer is supplied up front and
output frames are read in arbitrarily sized chunks.  Output frame ``k`` is
a pure function of ``k`` and the source samples, so reading in one large
block (offline) or many small blocks (streaming) yields bit-identical
results — the fractional phase is carried by the read cursor, and the
filter tail at the end of the stream is covered by zero-padding the source
boundaries.

Output length is ``ceil(source_frames * dst_rate / src_rate)``.
"""

from __future__ import annotations

import math

import numpy as np

NUM_TAPS = 64
_HALF = NUM_TAPS // 2

# Blackman window over the taps, fixed per tap index.
_WINDOW = np.blackman(NUM_TAPS)
_TAP_INDEX = np.arange(NUM_TAPS)


def _weights(frac: np.ndarray) -> np.ndarray:
    """Windowed-sinc weights, shape (len(frac), NUM_TAPS).

    Tap ``j`` multiplies source sample ``floor(pos) - 31 + j``; the sinc
    argument is therefore ``frac + 31 - j``.
    """
    x = frac[:, None] + (_HALF - 1 - _TAP_INDEX)[None, :]
    return np.sinc(x) * _WINDOW[None, :]


class Resampler:
    """Stateful pull resampler over an in-memory (frames, channels) buffer."""

    def __init__(self, source: np.ndarray, src_rate: int, dst_rate: int):
        if src_rate <= 0 or dst_rate <= 0:
            raise ValueError("sample rates must be positive")
        self._src = np.ascontiguousarray(source, dtype=np.float64)
        self._src_rate = int(src_rate)
        self._dst_rate = int(dst_rate)
        self._channels = self._src.shape[1]
        self._pos = 0  # next output frame index (the preserved phase)

    @property
    def total_frames(self) -> int:
        return int(math.ceil(self._src.shape[0] * self._dst_rate / self._src_rate))

    @property
    def frames_read(self) -> int:
        return self._pos

    @property
    def exhausted(self) -> bool:
        return self._pos >= self.total_frames

    def reset(self) -> None:
        self._pos = 0

    def read(self, num_frames: int) -> np.ndarray:
        """Read up to ``num_frames`` output frames (fewer at end of stream)."""
        if num_frames <= 0:
            raise ValueError("num_frames must be positive")
        n = min(num_frames, self.total_frames - self._pos)
        if n <= 0:
            return np.zeros((0, self._channels), dtype=np.float64)
        k0 = self._pos
        self._pos += n
        if self._src_rate == self._dst_rate:
            return self._src[k0 : k0 + n].copy()
        k = k0 + np.arange(n, dtype=np.float64)
        pos = k * (self._src_rate / self._dst_rate)
        base = np.floor(pos)
        frac = pos - base
        i0 = base.astype(np.int64) - (_HALF - 1)
        idx = i0[:, None] + _TAP_INDEX[None, :]  # (n, NUM_TAPS)
        valid = (idx >= 0) & (idx < self._src.shape[0])
        gathered = self._src[np.clip(idx, 0, self._src.shape[0] - 1)]
        w = _weights(frac) * valid
        # (n, taps, 1) * (n, taps, ch) -> sum over taps -> (n, ch)
        return np.einsum("ntc,nt->nc", gathered, w)


def resample(source: np.ndarray, src_rate: int, dst_rate: int) -> np.ndarray:
    """One-shot resample of a whole buffer (used by the offline path)."""
    r = Resampler(source, src_rate, dst_rate)
    return r.read(r.total_frames)
