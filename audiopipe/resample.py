"""Self-written polyphase windowed-sinc resampling kernel.

No third-party resampling library is used.  The resampler works on
interleaved frame/channel arrays of shape ``(frames, channels)``, keeps
filter history and polyphase state across chunks, and shares one bounded,
thread-safe coefficient cache for identical rate pairs inside a batch.

For an output sample ``m`` the corresponding input time is
``t = floor(m*down/up)`` with polyphase index ``p = (m*down) mod up``,
where ``up/down`` is the reduced target/source rate fraction.  Branch
``p`` is a symmetric Hann-windowed fractional-delay sinc evaluated on the
half-integer input grid centred on delay ``p/up``; its cutoff narrows to
the output Nyquist band when down-sampling (anti-aliasing) and stays at
the input Nyquist when up-sampling (anti-imaging).

Internally a cumulative buffer is kept with ``taps`` leading zero rows so
that buffer row ``t + taps`` always holds global input frame ``t``.  Old
rows are trimmed once no pending output can reference them, keeping
memory bounded to roughly one chunk plus two filter windows.
"""

from __future__ import annotations

import collections
import math
import threading
from fractions import Fraction

import numpy as np


def expected_output_frames(input_frames: int, source_rate: int,
                           target_rate: int) -> int:
    """Exact output length mandated by the pipeline contract."""
    ratio = Fraction(target_rate, source_rate)
    return int(math.floor(input_frames * ratio.numerator /
                          ratio.denominator + 0.5))


def _build_phases(up: int, down: int, taps: int) -> np.ndarray:
    """Build ``up`` symmetric polyphase branches, each of length ``2*taps``.

    Coefficient column ``k`` (half-integer offsets from ``-taps`` to
    ``taps-1``) weights the input at that offset from the branch centre.
    Every branch is normalized to unit DC gain after the full symmetric
    kernel is windowed, preserving the window's stopband attenuation.
    """
    fc = 0.5 * min(1.0, up / down)
    offsets = np.arange(-taps, taps, dtype=np.float64) + 0.5
    window = np.hanning(2 * taps)
    phases = np.empty((up, 2 * taps), dtype=np.float64)
    for p in range(up):
        branch = np.sinc(2.0 * fc * (offsets - p / up)) * window
        phases[p] = branch / branch.sum()
    return phases


class KernelCache:
    """Bounded, thread-safe LRU cache of polyphase coefficient tables."""

    def __init__(self, max_entries: int = 64):
        if max_entries < 1:
            raise ValueError("max_entries must be >= 1")
        self._lock = threading.RLock()
        self._data: "collections.OrderedDict[tuple, tuple]" = (
            collections.OrderedDict())
        self.max_entries = max_entries

    def get(self, source_rate: int, target_rate: int,
            taps: int) -> tuple[np.ndarray, int, int]:
        """Return ``(phases, up, down)``, building and caching on miss."""
        key = (int(source_rate), int(target_rate), int(taps))
        with self._lock:
            if key in self._data:
                self._data.move_to_end(key)
                up, down, phases = self._data[key]
                return phases, up, down
        ratio = Fraction(target_rate, source_rate)
        up, down = ratio.numerator, ratio.denominator
        phases = _build_phases(up, down, taps)
        with self._lock:
            if key in self._data:
                self._data.move_to_end(key)
                return self._data[key][2], up, down
            self._data[key] = (up, down, phases)
            while len(self._data) > self.max_entries:
                self._data.popitem(last=False)
            return phases, up, down

    def __len__(self) -> int:
        with self._lock:
            return len(self._data)


class Resampler:
    """Streaming polyphase resampler for one stream."""

    def __init__(self, source_rate: int, target_rate: int, channels: int,
                 taps: int = 32, cache: KernelCache | None = None):
        if channels < 1:
            raise ValueError("channels must be >= 1")
        if taps < 1:
            raise ValueError("taps must be >= 1")
        self.source_rate = int(source_rate)
        self.target_rate = int(target_rate)
        self.channels = int(channels)
        self.taps = int(taps)
        cache = cache if cache is not None else KernelCache()
        self._phases, self._up, self._down = cache.get(
            self.source_rate, self.target_rate, self.taps)
        self._identity = self._up == 1 and self._down == 1
        self._in_count = 0
        self._out_count = 0
        # Row `taps + offset` holds global frame `offset`; `_offset` is
        # the global frame represented at buffer row 0 after trimming.
        self._offset = -self.taps
        self._buf = np.zeros((self.taps, channels), dtype=np.float64)

    def _emit(self, centre_limit: int) -> np.ndarray:
        """Drain outputs whose full 2*taps window is inside the buffer."""
        indices: list[int] = []
        phases: list[int] = []
        m = self._out_count
        n_rows = self._buf.shape[0]
        while True:
            t, phase = divmod(m * self._down, self._up)
            local_t = t - self._offset
            if local_t + self.taps >= n_rows:
                break
            indices.append(local_t)
            phases.append(phase)
            m += 1
        self._out_count = m
        if not indices:
            return np.empty((0, self.channels), dtype=np.float64)
        idx = np.asarray(indices, dtype=np.int64)
        offsets = np.arange(-self.taps + 1, self.taps + 1, dtype=np.int64)
        selected = self._buf[idx[:, None] + offsets[None, :]]
        coefs = self._phases[np.asarray(phases, dtype=np.int64)]
        return np.einsum("mk,mkc->mc", coefs, selected)

    def _trim(self) -> None:
        """Drop rows no pending output can reach, keeping memory bounded."""
        if self._out_count >= self._in_count and self._in_count > 0:
            return
        t_next, _ = divmod(self._out_count * self._down, self._up)
        # Oldest global frame the next output still reads is t_next-taps+1.
        keep_global = t_next - self.taps + 1
        cut = keep_global - self._offset
        if cut > 0:
            self._buf = self._buf[cut:]
            self._offset += cut

    def process(self, block: np.ndarray) -> np.ndarray:
        """Resample one chunk; returns zero or more output frames."""
        block = np.asarray(block, dtype=np.float64)
        if block.ndim == 1:
            block = block[:, None]
        if block.shape[1] != self.channels:
            raise ValueError("channel count mismatch")
        if self._identity:  # same sample rate: exact passthrough
            self._in_count += block.shape[0]
            self._out_count += block.shape[0]
            return block.copy()
        self._buf = np.concatenate([self._buf, block], axis=0)
        self._in_count += block.shape[0]
        out = self._emit(self._in_count)
        self._trim()
        return out

    def flush(self) -> tuple[np.ndarray, int]:
        """Zero-pad the tail, drain it, trim to the contract length.

        Returns ``(output, zero_pad_frames)``; padded zeros never yield
        output beyond the mandated frame count.
        """
        wanted = expected_output_frames(
            self._in_count, self.source_rate, self.target_rate)
        if self._identity:
            return (np.empty((0, self.channels), dtype=np.float64), 0)
        start_out = self._out_count
        # Virtual zero frames extend the buffer until every contracted
        # output can be evaluated; one filter window of padding suffices
        # once the loop stops making progress.
        pad_frames = 0
        while self._out_count < wanted and pad_frames <= 2 * self.taps:
            pad_frames += 1
            self._buf = np.concatenate(
                [self._buf, np.zeros((1, self.channels), dtype=np.float64)],
                axis=0)
            self._emit(self._in_count + pad_frames)
        # Rounding can let one centre past the contract; never emit it.
        if self._out_count > wanted:
            self._out_count = wanted
        tail_count = self._out_count - start_out
        # Recompute the tail outputs as one contiguous slice for return.
        tail = self._tail_outputs(start_out, tail_count)
        return tail, pad_frames

    def _tail_outputs(self, start_out: int, count: int) -> np.ndarray:
        """Materialize outputs ``start_out .. start_out+count-1``."""
        if count <= 0:
            return np.empty((0, self.channels), dtype=np.float64)
        indices: list[int] = []
        phases: list[int] = []
        for m in range(start_out, start_out + count):
            t, phase = divmod(m * self._down, self._up)
            indices.append(t - self._offset)
            phases.append(phase)
        idx = np.asarray(indices, dtype=np.int64)
        offsets = np.arange(-self.taps + 1, self.taps + 1, dtype=np.int64)
        selected = self._buf[idx[:, None] + offsets[None, :]]
        coefs = self._phases[np.asarray(phases, dtype=np.int64)]
        return np.einsum("mk,mkc->mc", coefs, selected)


def resample_whole(signal: np.ndarray, source_rate: int, target_rate: int,
                   channels: int, taps: int = 32,
                   cache: KernelCache | None = None) -> np.ndarray:
    """Convenience helper: resample a complete in-memory signal."""
    rs = Resampler(source_rate, target_rate, channels, taps=taps,
                   cache=cache)
    head = rs.process(signal)
    tail, _ = rs.flush()
    return np.concatenate([head, tail], axis=0)
