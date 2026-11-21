"""Windowed-sinc resampling with a deterministic, chunk-safe state machine.

Output sample ``k`` sits at source position ``k * rate_in / rate_out``. All
positions are derived with integer arithmetic from the absolute output index,
so the produced samples do not depend on how the input was split into chunks.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np

from .model import resampled_frames as resampled_length

ZERO_CROSSINGS = 16
MAX_HALF_WIDTH = ZERO_CROSSINGS * 8


def _window(distance: np.ndarray, half_width: int) -> np.ndarray:
    """Blackman window over ``[-half_width, half_width]``."""
    inside = np.abs(distance) <= half_width
    ratio = distance / half_width
    value = 0.42 + 0.5 * np.cos(np.pi * ratio) + 0.08 * np.cos(2.0 * np.pi * ratio)
    return np.where(inside, value, 0.0)


def kernel_weights(distance: np.ndarray, half_width: int, cutoff: float) -> np.ndarray:
    """Windowed-sinc low-pass tap weights at fractional source distances."""
    return cutoff * np.sinc(cutoff * distance) * _window(distance, half_width)


@dataclass(frozen=True)
class Kernel:
    half_width: int
    cutoff: float

    @classmethod
    def for_rates(cls, rate_in: int, rate_out: int) -> "Kernel":
        if rate_in <= 0 or rate_out <= 0:
            raise ValueError("sample rates must be positive")
        ratio = rate_out / rate_in
        cutoff = min(1.0, ratio)
        half_width = min(int(np.ceil(ZERO_CROSSINGS / cutoff)), MAX_HALF_WIDTH)
        return cls(half_width=half_width, cutoff=cutoff)

    def weights(self, frac: np.ndarray) -> np.ndarray:
        return kernel_weights(frac, self.half_width, self.cutoff)


class StreamingResampler:
    """Resample a source of arbitrary length in chunks of arbitrary size.

    ``pull(chunk)`` accepts new source frames and returns the output frames
    whose full filter support is now available. ``finalize()`` emits the tail,
    replicating the final source sample at the edge. Together they produce
    exactly :func:`resampled_frames` frames.
    """

    def __init__(self, rate_in: int, rate_out: int, channels: int):
        if channels not in (1, 2):
            raise ValueError("only mono and stereo sources are supported")
        self.rate_in = rate_in
        self.rate_out = rate_out
        self.channels = channels
        self.kernel = Kernel.for_rates(rate_in, rate_out)
        self.rim = self.kernel.half_width
        self.history = np.zeros((channels, 0), dtype=np.float64)
        self.history_origin = 0
        self.source_frames = 0
        self.next_output = 0
        self.finished = False
        # Edge replication: taps outside the source see the boundary sample
        # rather than silence, so a DC signal stays flat at both ends.
        self.edge_first = np.zeros(channels, dtype=np.float64)
        self.edge_last = np.zeros(channels, dtype=np.float64)

    @property
    def history_frames(self) -> int:
        return int(self.history.shape[1])

    def _trim(self, first_center: int) -> None:
        keep_from = max(0, first_center - self.rim)
        if keep_from <= self.history_origin:
            return
        drop = min(keep_from - self.history_origin, self.history_frames)
        if drop >= self.history_frames:
            self.history = np.zeros((self.channels, 0), dtype=np.float64)
        else:
            self.history = self.history[:, drop:]
        self.history_origin += drop

    def _emit(self, limit: int, final: bool) -> np.ndarray:
        count = limit - self.next_output
        if count <= 0:
            return np.zeros((self.channels, 0), dtype=np.float64)
        ks = np.arange(self.next_output, limit, dtype=np.int64)
        numerators = ks * self.rate_in
        centers = numerators // self.rate_out
        frac = (numerators % self.rate_out).astype(np.float64) / float(self.rate_out)
        self.next_output = limit

        self._trim(int(centers[0]))
        local = centers - self.history_origin
        needed = int(local.max()) + self.rim + 1
        if needed > self.history_frames and not final:
            raise RuntimeError("internal error: resample history underrun")
        if self.history_frames == 0:
            raise RuntimeError("internal error: resample history is empty")

        out = np.zeros((self.channels, count), dtype=np.float64)
        half = self.kernel.half_width
        for offset in range(-half, half + 1):
            weights = self.kernel.weights(frac - float(offset))
            offset_local = local + offset
            inside = (offset_local >= 0) & (offset_local < self.history_frames)
            before = offset_local < 0
            after = offset_local >= self.history_frames
            values = np.empty((self.channels, count), dtype=np.float64)
            safe = np.clip(offset_local, 0, self.history_frames - 1)
            values[:] = self.history[:, safe]
            if np.any(before):
                values[:, before] = self.edge_first[:, None]
            if np.any(after):
                values[:, after] = self.edge_last[:, None]
            out += values * weights[None, :]
        return out

    def pull(self, new_frames: np.ndarray) -> np.ndarray:
        """Feed new source frames shaped ``(channels, n)``; return output."""
        if self.finished:
            raise RuntimeError("resampler already finalized")
        new_frames = np.asarray(new_frames, dtype=np.float64)
        if new_frames.ndim != 2 or new_frames.shape[0] != self.channels:
            raise ValueError("source chunk has the wrong channel layout")
        if new_frames.shape[1] == 0:
            return np.zeros((self.channels, 0), dtype=np.float64)
        if self.history_frames == 0:
            self.history = new_frames.copy()
            self.history_origin = self.source_frames
            self.edge_first = new_frames[:, 0].copy()
        else:
            self.history = np.concatenate([self.history, new_frames], axis=1)
        self.edge_last = new_frames[:, -1].copy()
        self.source_frames += new_frames.shape[1]

        # Largest k whose centre has full support inside known source frames.
        max_center = self.source_frames - 1 - self.rim
        if max_center < 0:
            return np.zeros((self.channels, 0), dtype=np.float64)
        limit = ((max_center + 1) * self.rate_out - 1) // self.rate_in
        return self._emit(limit, final=False)

    def finalize(self) -> np.ndarray:
        """Emit the tail, replicating the final source sample at the edge."""
        if self.finished:
            return np.zeros((self.channels, 0), dtype=np.float64)
        self.finished = True
        if self.source_frames == 0:
            return np.zeros((self.channels, 0), dtype=np.float64)
        total = resampled_length(self.source_frames, self.rate_in, self.rate_out)
        return self._emit(total, final=True)
