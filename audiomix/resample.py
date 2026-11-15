"""Windowed-sinc resampler with persistent filter history.

The resampler converts a stream of input frames at ``src_rate`` into frames at
``dst_rate`` while keeping state across calls, so feeding the signal one block
at a time yields exactly the same samples as feeding it all at once.  A
Hann-windowed sinc kernel (half width :data:`HALF_TAPS` taps on each side)
provides anti-alias filtering; input samples that have not arrived yet, or
have already been flushed, are treated as zero.

The number of output samples is derived from integer frame counters rather
than accumulated floating point positions, which makes the final flush exact
for rational rate pairs (e.g. 44100 -> 48000).
"""

from __future__ import annotations

import math

import numpy as np

from . import AudioProcessError

HALF_TAPS = 16


def _sinc(x: float) -> float:
    """Normalized sinc: sin(pi*x)/(pi*x), with sinc(0) = 1."""
    if x == 0.0:
        return 1.0
    px = math.pi * x
    return math.sin(px) / px


class SincResampler:
    """Streaming arbitrary-ratio resampler for 1-D or 2-D float arrays.

    Input arrays may be shape ``(n,)`` (mono) or ``(n, channels)``.  All
    internal arithmetic is float64.
    """

    def __init__(self, src_rate: int, dst_rate: int, half_taps: int = HALF_TAPS):
        if src_rate <= 0 or dst_rate <= 0:
            raise AudioProcessError(
                f"resampler rates must be positive, got {src_rate} -> {dst_rate}"
            )
        self.src_rate = int(src_rate)
        self.dst_rate = int(dst_rate)
        self.half_taps = int(half_taps)
        self.ratio = self.dst_rate / self.src_rate
        self._passthrough = self.src_rate == self.dst_rate
        # Input history plus unconsumed input, newest sample last.
        self._buffer = np.empty((0, 0), dtype=np.float64)
        self._channels = 0
        # Total input frames ever presented (before the buffer's oldest frame).
        self._input_count = 0
        self._emitted = 0

    # ------------------------------------------------------------------ #
    def _ensure_shape(self, block: np.ndarray) -> np.ndarray:
        arr = np.asarray(block, dtype=np.float64)
        if arr.ndim == 1:
            arr = arr[:, np.newaxis]
        elif arr.ndim != 2:
            raise AudioProcessError(
                f"resampler expects 1-D or 2-D arrays, got {arr.ndim}-D"
            )
        if self._channels == 0:
            self._channels = arr.shape[1]
            self._buffer = np.empty((0, self._channels), dtype=np.float64)
        elif arr.shape[1] != self._channels:
            raise AudioProcessError(
                f"resampler channel count changed: {self._channels} -> {arr.shape[1]}"
            )
        return arr

    def _append(self, block: np.ndarray) -> None:
        arr = self._ensure_shape(block)
        if arr.shape[0]:
            self._buffer = np.concatenate((self._buffer, arr), axis=0)
            self._input_count += arr.shape[0]

    def _center(self, output_index: int) -> float:
        """Input-frame position of output sample ``output_index`` (0-based)."""
        return (output_index + 0.5) / self.ratio - 0.5

    def _expected_outputs(self) -> int:
        """Output count fully determined by the inputs received so far."""
        return (self._input_count * self.dst_rate) // self.src_rate

    def _emit(self, limit: int | None, final: bool = False) -> np.ndarray:
        """Produce outputs whose input window is fully available.

        During streaming (``final=False``) an output is only emitted once the
        highest kernel tap has been fed, so not-yet-arrived samples are never
        mistaken for zeros.  On flush (``final=True``) missing taps are real
        tail zeros and emission runs up to the exact expected output count.
        """
        out: list[np.ndarray] = []
        ht = self.half_taps
        n_in = self._buffer.shape[0]
        base = self._input_count - n_in  # input index of buffer row 0
        total = self._expected_outputs() if final else None
        while True:
            idx = self._emitted
            if limit is not None and idx >= limit:
                break
            if final and idx >= total:
                break
            center = self._center(idx)
            hi = int(math.floor(center)) + ht
            if not final and hi >= self._input_count:
                # Right-side kernel taps have not been fed yet.
                break
            lo = int(math.floor(center)) - ht + 1
            acc = np.zeros(self._channels, dtype=np.float64)
            weight_sum = 0.0
            for k in range(lo, hi + 1):
                x = center - k
                window = 0.5 + 0.5 * math.cos(math.pi * x / ht)
                w = window * _sinc(x)
                row = k - base
                if 0 <= row < n_in:
                    acc += w * self._buffer[row]
                    weight_sum += w
            if weight_sum != 0.0:
                acc /= weight_sum
            out.append(acc)
            self._emitted += 1
        if not out:
            return np.empty((0, self._channels), dtype=np.float64)
        return np.stack(out, axis=0)

    def _trim(self) -> None:
        """Drop input samples no future output kernel can reach."""
        n_in = self._buffer.shape[0]
        if n_in == 0:
            return
        next_center = self._center(self._emitted)
        keep_from = int(math.floor(next_center)) - self.half_taps + 1
        base = self._input_count - n_in
        drop = max(0, keep_from - base)
        if drop:
            self._buffer = self._buffer[drop:]

    # ------------------------------------------------------------------ #
    def process(self, block: np.ndarray, max_output: int | None = None) -> np.ndarray:
        """Feed one input block and return every newly available output.

        ``max_output`` optionally caps the returned frame count (streaming
        mode); remaining outputs stay buffered and surface on later calls.
        """
        if self._passthrough:
            arr = np.asarray(block, dtype=np.float64)
            if arr.ndim == 1:
                count = arr.shape[0]
            elif arr.ndim == 2:
                count = arr.shape[0]
                if self._channels == 0:
                    self._channels = arr.shape[1]
                elif arr.shape[1] != self._channels:
                    raise AudioProcessError(
                        f"resampler channel count changed: {self._channels} -> {arr.shape[1]}"
                    )
            else:
                raise AudioProcessError(
                    f"resampler expects 1-D or 2-D arrays, got {arr.ndim}-D"
                )
            self._input_count += count
            self._emitted += count
            if max_output is not None and count > max_output:
                raise AudioProcessError("passthrough resampler cannot split block")
            return arr.copy()
        self._append(block)
        out = self._emit(
            None if max_output is None else self._emitted + max_output,
            final=False,
        )
        self._trim()
        return out

    def flush(self, max_output: int | None = None) -> np.ndarray:
        """Return the tail samples whose input has already been received."""
        if self._passthrough:
            return np.empty((0, self._channels), dtype=np.float64)
        target = self._expected_outputs()
        out = self._emit(
            None if max_output is None else self._emitted + max_output,
            final=True,
        )
        if max_output is None:
            if self._emitted != target:  # defensive; should never happen
                raise AudioProcessError(
                    f"resampler flush produced {self._emitted}/{target} outputs"
                )
        self._trim()
        return out

    @property
    def emitted(self) -> int:
        return self._emitted
