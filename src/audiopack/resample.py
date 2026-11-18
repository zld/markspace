"""Polyphase windowed-sinc resampling kernel with anti-aliasing lowpass.

The ratio is reduced to exact integers ``up/down``; the fractional phase of each
output frame is maintained with integer arithmetic, so endpoint phase is
continuous and output length is deterministic. Samples required outside the
input range are treated as zero.
"""

from __future__ import annotations

import math

import numpy as np


def output_length(frames_in: int, src_rate: int, dst_rate: int) -> int:
    """Output frame count: input frames scaled by the rate ratio, rounded."""
    g = math.gcd(src_rate, dst_rate)
    up, down = dst_rate // g, src_rate // g
    return (2 * frames_in * up + down) // (2 * down)


class ResamplerKernel:
    """Reusable polyphase resampler for a fixed (src_rate, dst_rate) pair."""

    def __init__(
        self,
        src_rate: int,
        dst_rate: int,
        taps_per_phase: int = 16,
        cutoff_scale: float = 0.95,
    ) -> None:
        if src_rate <= 0 or dst_rate <= 0:
            raise ValueError("sample rates must be positive")
        if taps_per_phase < 4 or taps_per_phase % 2:
            raise ValueError("taps_per_phase must be an even integer >= 4")
        self.src_rate = src_rate
        self.dst_rate = dst_rate
        g = math.gcd(src_rate, dst_rate)
        self.up = dst_rate // g
        self.down = src_rate // g
        self.half = taps_per_phase // 2
        self.passthrough = self.up == 1 and self.down == 1
        # Polyphase matrix: taps[phase, j] = h[j * up + phase].
        self.taps = self._design(cutoff_scale) if not self.passthrough else None
        # Per-phase first output index m0 with (m0 * down) % up == phase, and
        # the corresponding input offset q0 = (m0 * down) // up.
        self._phase_m0: list[int] = []
        self._phase_q0: list[int] = []
        if not self.passthrough:
            inv_down = pow(self.down, -1, self.up)
            for phase in range(self.up):
                m0 = (phase * inv_down) % self.up
                self._phase_m0.append(m0)
                self._phase_q0.append((m0 * self.down) // self.up)

    def _design(self, cutoff_scale: float) -> np.ndarray:
        """Windowed-sinc lowpass at the upsampled rate, decomposed by phase."""
        up = self.up
        ncols = 2 * self.half + 1
        n = ncols * up  # padded so every phase row has ncols taps
        center = self.half * up
        fc = cutoff_scale * 0.5 / max(self.up, self.down)  # cycles/upsampled sample
        idx = np.arange(n, dtype=np.float64) - center
        h = np.sinc(2.0 * fc * idx) * (2.0 * fc)
        h *= np.blackman(n)
        h *= self.up / h.sum()  # unity DC gain per polyphase branch
        taps = np.zeros((up, ncols), dtype=np.float64)
        for phase in range(up):
            taps[phase, :] = h[phase::up]
        return taps

    def resample_channel(self, x: np.ndarray) -> np.ndarray:
        """Resample one mono channel (1-D array) to the target rate."""
        x = np.ascontiguousarray(x, dtype=np.float64)
        n_in = x.shape[0]
        n_out = output_length(n_in, self.src_rate, self.dst_rate)
        if self.passthrough:
            return x[:n_out].copy()
        y = np.zeros(n_out, dtype=np.float64)
        taps = self.taps
        assert taps is not None
        ncols = taps.shape[1]
        for phase in range(self.up):
            m0 = self._phase_m0[phase]
            if m0 >= n_out:
                continue
            ms = np.arange(m0, n_out, self.up)
            q = self._phase_q0[phase] + (ms - m0) // self.up * self.down
            acc = np.zeros(ms.shape[0], dtype=np.float64)
            for j in range(ncols):
                idx = q + (self.half - j)
                valid = (idx >= 0) & (idx < n_in)
                if valid.any():
                    acc[valid] += taps[phase, j] * x[idx[valid]]
            y[ms] = acc
        return y

    def resample(self, frames: np.ndarray) -> np.ndarray:
        """Resample an interleaved (frames, channels) buffer, per channel."""
        if frames.ndim != 2:
            raise ValueError("expected a (frames, channels) array")
        n_out = output_length(frames.shape[0], self.src_rate, self.dst_rate)
        if self.passthrough:
            return frames[:n_out].astype(np.float64, copy=True)
        out = np.empty((n_out, frames.shape[1]), dtype=np.float64)
        for ch in range(frames.shape[1]):
            out[:, ch] = self.resample_channel(frames[:, ch])
        return out
