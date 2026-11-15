"""Stateful polyphase resampling with an anti-aliasing FIR filter.

The resampler up-samples by ``up``, filters with a windowed-sinc
low-pass (cutoff below the Nyquist of the lower rate, so it acts as
both anti-imaging and anti-aliasing filter), and picks every
``down``-th sample.  All phase arithmetic uses exact integers, so the
output for a given output-frame index is bit-identical no matter how
the input was chunked -- this is what lets the offline and streaming
executors agree to well below 1e-6.

At end of input the missing tail samples are treated as zeros (the
filter tail is flushed), and the total output frame count follows the
rounding rule ``round(in_frames * out_rate / in_rate)`` (half up).
"""

from __future__ import annotations

from fractions import Fraction

import numpy as np

TAPS_PER_PHASE = 16


def output_frames(in_frames: int, in_rate: int, out_rate: int) -> int:
    """Total output frames for ``in_frames`` of input, rounded half up."""
    ratio = Fraction(out_rate, in_rate)
    num = in_frames * ratio.numerator
    den = ratio.denominator
    return (2 * num + den) // (2 * den)


def design_lowpass(up: int, down: int) -> np.ndarray:
    """Windowed-sinc low-pass for the polyphase interpolator."""
    m = max(up, down)
    half = (TAPS_PER_PHASE // 2) * m
    n = np.arange(-half, half + 1, dtype=np.float64)
    fc = 0.45 / m  # cycles per upsampled sample, below both Nyquists
    h = 2.0 * fc * np.sinc(2.0 * fc * n)
    h *= np.hanning(2 * half + 1)
    h *= up / h.sum()
    return h


class StreamResampler:
    """Incremental resampler; feed input blocks, read output blocks."""

    def __init__(self, in_rate: int, out_rate: int, channels: int, in_frames: int):
        if in_rate <= 0 or out_rate <= 0:
            raise ValueError("sample rates must be positive")
        if channels < 1:
            raise ValueError("channels must be >= 1")
        ratio = Fraction(out_rate, in_rate)
        self.up, self.down = ratio.numerator, ratio.denominator
        self.h = design_lowpass(self.up, self.down)
        self.channels = channels
        self.in_frames = in_frames
        self.total_out = output_frames(in_frames, in_rate, out_rate)
        self._buf = np.zeros((0, channels))
        self._buf_start = 0  # absolute input index of _buf[0]
        self._fed = 0
        self._eof = False
        self._n_out = 0

    @property
    def fed(self) -> int:
        return self._fed

    @property
    def emitted(self) -> int:
        return self._n_out

    def feed(self, block: np.ndarray) -> None:
        if self._eof:
            raise RuntimeError("feed after finish")
        a = np.ascontiguousarray(block, dtype=np.float64)
        if a.ndim == 1:
            a = a[:, None]
        if a.shape[1] != self.channels:
            raise ValueError(f"expected {self.channels} channels, got {a.shape[1]}")
        if len(a):
            self._buf = np.concatenate((self._buf, a), axis=0)
            self._fed += len(a)
        if self._fed > self.in_frames:
            raise ValueError("fed more input than declared")

    def finish(self) -> None:
        """Mark end of input; the filter tail is flushed with zeros."""
        self._eof = True

    def _emittable(self) -> int:
        if self._eof:
            return self.total_out - self._n_out
        # output n needs input through (n*down)//up, so n*down < fed*up
        return max(0, (self._fed * self.up + self.down - 1) // self.down - self._n_out)

    def read(self, count: int) -> np.ndarray:
        n = min(count, self._emittable())
        if n <= 0:
            return np.zeros((0, self.channels))
        out = self._emit(self._n_out, self._n_out + n)
        self._n_out += n
        self._discard()
        return out

    def _emit(self, n0: int, n1: int) -> np.ndarray:
        up, down, h = self.up, self.down, self.h
        size = len(h)
        n = np.arange(n0, n1, dtype=np.int64)
        nd = n * down
        taps = (size + up - 1) // up + 1
        k = (nd // up)[:, None] - np.arange(taps)[None, :]  # input indices
        j = nd[:, None] - k * up                            # filter phases
        w = np.where((j >= 0) & (j < size), h[np.clip(j, 0, size - 1)], 0.0)
        lo, hi = self._buf_start, self._buf_start + len(self._buf)
        valid = (k >= lo) & (k < hi)  # missing samples read as zeros
        if hi > lo:
            kc = np.clip(k, lo, hi - 1) - lo
            samples = self._buf[kc] * valid[..., None]
        else:
            samples = np.zeros((len(n), taps, self.channels))
        return (samples * w[..., None]).sum(axis=1)

    def _discard(self) -> None:
        # smallest input index any future output can still touch
        nd = self._n_out * self.down
        kmin = -((len(self.h) - 1 - nd) // self.up)
        drop = min(max(0, kmin - self._buf_start), len(self._buf))
        if drop:
            self._buf = self._buf[drop:]
            self._buf_start += drop
