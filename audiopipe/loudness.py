"""Loudness and level statistics implementing ITU-R BS.1770-4.

The meter is streaming: feed interleaved ``(frames, channels)`` chunks
with :meth:`add`, then call :meth:`integrated_lufs`.  K-weighting uses
the two biquad stages published in BS.1770-4; measurements run at the
48 kHz grid because the published coefficients are defined there, so
non-48 kHz material is resampled by the caller before metering (the
pipeline always meters at the output sample rate).

Integrated loudness uses 400 ms rectangular blocks advanced every
100 ms (75% overlap), channel weights L/R/C/Ls/Rs = 1.0 and LFE
excluded, an absolute gate at -70 LKFS and a relative gate at
-10 LU below the absolute-gated mean.  Silent material reports
``None`` (serialized as JSON ``null``).
"""

from __future__ import annotations

import math

import numpy as np

#: BS.1770-4 K-weighting biquad coefficients, defined at 48 kHz.
_STAGE1_B = (1.53512485958697, -2.69169618940638, 1.19839281085285)
_STAGE1_A = (1.0, -1.69065929318241, 0.73248077421585)
_STAGE2_B = (1.0, -2.0, 1.0)
_STAGE2_A = (1.0, -1.99004745483398, 0.99007225036621)

#: Channel weights by layout position; LFE is absent (excluded).
CHANNEL_WEIGHTS = {
    1: (1.0,),
    2: (1.0, 1.0),
    6: (1.0, 1.0, 1.0, 0.0, 1.0, 1.0),
}

BLOCK_SECONDS = 0.4
HOP_SECONDS = 0.1
ABSOLUTE_GATE_LKFS = -70.0
RELATIVE_GATE_LU = -10.0
MINUS_INFINITY_LUFS = -70.0  # BS.1770 defines silence at/below this.


class _Biquad:
    """Transposed direct-form-II biquad with per-channel state.

    Each block is solved by superposition: the zero-state forced response
    uses a short scalar recurrence within the (small) block, while the
    response from the carried state is a matrix power of the companion
    matrix evaluated once per block size and applied with vectorized
    NumPy.  This keeps the filter exact (~1e-14 vs the scalar form) while
    avoiding a Python iteration per audio sample.
    """

    BLOCK = 256

    def __init__(self, b: tuple[float, ...], a: tuple[float, ...],
                 channels: int):
        self.b0, self.b1, self.b2 = b
        _, self.a1, self.a2 = a
        self.channels = channels
        self.w1 = np.zeros(channels, dtype=np.float64)
        self.w2 = np.zeros(channels, dtype=np.float64)
        companion = np.array(
            [[-self.a1, -self.a2], [1.0, 0.0]], dtype=np.float64)
        powers = [np.eye(2, dtype=np.float64)]
        for _ in range(self.BLOCK):
            powers.append(companion @ powers[-1])
        # P[k] = companion^k, shape (BLOCK+1, 2, 2).
        self._powers = np.stack(powers, axis=0)

    def process(self, x: np.ndarray) -> np.ndarray:
        out = np.empty_like(x)
        a1, a2 = self.a1, self.a2
        b0, b1, b2 = self.b0, self.b1, self.b2
        for start in range(0, x.shape[0], self.BLOCK):
            block = x[start:start + self.BLOCK]
            n = block.shape[0]
            powers = self._powers[:n + 1]
            # Homogeneous state evolution from the carried (w1, w2).
            # init has shape (2, channels); hom[k] = powers[k] @ init.
            init = np.stack([self.w1, self.w2], axis=0)  # (2, channels)
            hom = np.einsum("kij,jc->kic", powers, init)
            # Zero-state forced response (scalar over a short block).
            forced = np.empty_like(block)
            f1 = np.zeros(self.channels, dtype=np.float64)
            f2 = np.zeros(self.channels, dtype=np.float64)
            for i in range(n):
                w0 = block[i] - a1 * f1 - a2 * f2
                forced[i] = w0
                f2, f1 = f1, w0
            zero_row = np.zeros((1, self.channels), dtype=np.float64)
            f_prev1 = np.concatenate([zero_row, forced[:-1]], axis=0)
            if n == 1:
                f_prev2 = zero_row
            else:
                f_prev2 = np.concatenate(
                    [zero_row, zero_row, forced[:-2]], axis=0)
            y_forced = b0 * forced + b1 * f_prev1 + b2 * f_prev2
            hw0 = hom[1:, 0]
            hw1 = hom[1:, 1]
            # Homogeneous w2 history: initial carried w2, then prior hw1.
            if n == 1:
                hw2 = self.w2[None, :]
            else:
                hw2 = np.concatenate(
                    [self.w2[None, :], hom[1:n, 1]], axis=0)
            y_hom = b0 * hw0 + b1 * hw1 + b2 * hw2
            out[start:start + n] = y_forced + y_hom
            # Carried state = forced end-state + homogeneous end-state.
            forced_w1 = forced[-1]
            forced_w2 = forced[-2] if n > 1 else np.zeros(
                self.channels)
            self.w1 = forced_w1 + hom[n, 0]
            self.w2 = forced_w2 + hom[n, 1]
        return out


def k_weight(signal: np.ndarray, channels: int,
             states: tuple[_Biquad, _Biquad] | None = None
             ) -> tuple[np.ndarray, tuple[_Biquad, _Biquad]]:
    """Apply the two BS.1770 K-weighting stages to one chunk."""
    if states is None:
        states = (_Biquad(_STAGE1_B, _STAGE1_A, channels),
                  _Biquad(_STAGE2_B, _STAGE2_A, channels))
    y = states[1].process(states[0].process(signal))
    return y, states


def linear_rms(signal: np.ndarray) -> float:
    """Linear RMS over all channels and frames; 0.0 for empty input."""
    if signal.size == 0:
        return 0.0
    return float(np.sqrt(np.mean(np.square(signal, dtype=np.float64))))


def count_clipped(signal: np.ndarray) -> int:
    """Count decoded samples with ``abs(x) >= 1`` (full-scale clip)."""
    if signal.size == 0:
        return 0
    return int(np.count_nonzero(np.abs(signal) >= 1.0))


class LoudnessMeter:
    """Streaming BS.1770-4 integrated loudness plus RMS/clip counters."""

    def __init__(self, channels: int, sample_rate: int):
        if channels not in CHANNEL_WEIGHTS:
            raise ValueError(f"unsupported channel count: {channels}")
        if sample_rate != 48000:
            raise ValueError(
                "BS.1770 K-weighting coefficients are defined at 48 kHz; "
                "resample before metering")
        self.channels = channels
        self.sample_rate = sample_rate
        self.block = int(round(BLOCK_SECONDS * sample_rate))
        self.hop = int(round(HOP_SECONDS * sample_rate))
        self._weights = np.asarray(CHANNEL_WEIGHTS[channels],
                                   dtype=np.float64)
        self._stages = (_Biquad(_STAGE1_B, _STAGE1_A, channels),
                        _Biquad(_STAGE2_B, _STAGE2_A, channels))
        self._carry = np.empty((0, channels), dtype=np.float64)
        self._mean_squares: list[float] = []
        self._sum_sq = 0.0
        self._n_samples = 0
        self._clipped = 0

    def add(self, chunk: np.ndarray) -> None:
        """Accumulate one interleaved float chunk of decoded samples."""
        chunk = np.asarray(chunk, dtype=np.float64)
        if chunk.ndim == 1:
            chunk = chunk[:, None]
        if chunk.shape[1] != self.channels:
            raise ValueError("channel count mismatch")
        if not np.all(np.isfinite(chunk)):
            raise ValueError("non-finite sample encountered")
        self._sum_sq += float(np.sum(np.square(chunk)))
        self._n_samples += chunk.size
        self._clipped += count_clipped(chunk)
        weighted, self._stages = k_weight(chunk, self.channels,
                                          self._stages)
        self._carry = np.concatenate([self._carry, weighted], axis=0)
        while self._carry.shape[0] >= self.block:
            frame = self._carry[:self.block]
            per_channel = np.mean(np.square(frame), axis=0)
            self._mean_squares.append(
                float(np.sum(self._weights * per_channel)))
            self._carry = self._carry[self.hop:]

    def rms(self) -> float:
        """Linear RMS across everything accumulated so far."""
        if self._n_samples == 0:
            return 0.0
        return math.sqrt(self._sum_sq / self._n_samples)

    def clipped(self) -> int:
        return self._clipped

    def block_loudnesses(self) -> list[float]:
        """Mean-square energy per 400 ms block (before gating)."""
        return list(self._mean_squares)

    def integrated_lufs(self) -> float | None:
        """BS.1770-4 gated integrated loudness; ``None`` when silent."""
        energies = np.asarray(self._mean_squares, dtype=np.float64)
        if energies.size == 0 or not np.any(
                energies > 0.0):
            return None
        with np.errstate(divide="ignore"):
            loudness = -0.691 + 10.0 * np.log10(energies)
        absolute_pass = loudness > ABSOLUTE_GATE_LKFS
        if not np.any(absolute_pass):
            return None
        gated_mean = float(np.mean(energies[absolute_pass]))
        relative_threshold = (-0.691 + 10.0 * math.log10(gated_mean)
                              + RELATIVE_GATE_LU)
        relative_pass = loudness > relative_threshold
        if not np.any(relative_pass):
            relative_pass = absolute_pass
        final_mean = float(np.mean(energies[relative_pass]))
        if final_mean <= 0.0:
            return None
        return -0.691 + 10.0 * math.log10(final_mean)
