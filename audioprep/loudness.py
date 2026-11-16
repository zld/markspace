"""K-weighted gated loudness (LUFS) and RMS, implemented from scratch.

The measurement follows the BS.1770 structure without calling any loudness
library:

1. **K-weighting** -- two biquad stages per channel: a high-shelf (stage 1,
   +4 dB emphasis) and a high-pass (stage 2), both as transposed direct form
   II IIR filters whose state streams block by block;
2. **gating** -- 400 ms rectangular windows advancing with a 75% overlap
   (100 ms hop). Block mean-square energies feed an absolute gate at -70 LKFS
   and a relative gate at (loudness of the absolute-gated blocks) - 10 LU;
3. **short material** -- non-empty audio shorter than one full window is
   measured once over its whole length and flagged as a fallback;
4. **silence** -- returns ``None`` (serialized as JSON ``null``) rather than
   emitting infinite or NaN dB values.

RMS is the unweighted full-signal mean square, converted to dBFS. All
calculations are double precision and frame-blocked: only filter state and
the overlapping window tail are retained between blocks.
"""

from __future__ import annotations

import math
from dataclasses import dataclass, field

import numpy as np

# BS.1770-4 K-weighting biquad coefficients (normalized angular frequency).
# Stage 1: high-shelf (+4 dB). Stage 2: high-pass.
STAGE1_B = (1.53512485958697, -2.69169618940638, 1.19839281085285)
STAGE1_A = (1.0, -1.69065929318241, 0.73248077421585)
STAGE2_B = (1.0, -2.0, 1.0)
STAGE2_A = (1.0, -1.99004745483398, 0.99007225036621)

WINDOW_MS = 400.0
HOP_MS = 100.0  # 75% overlap
ABSOLUTE_GATE_LKFS = -70.0
RELATIVE_GATE_OFFSET_LU = -10.0
ABSOLUTE_GATE_ENERGY = 10.0 ** (ABSOLUTE_GATE_LKFS / 10.0)  # 1e-7


def db_from_linear(energy: float) -> float | None:
    """Convert a mean-square energy to dBFS; ``None`` when silent/invalid."""
    if energy is None or not math.isfinite(energy) or energy <= 0.0:
        return None
    return 10.0 * math.log10(energy)


def rms_dbfs(signal: np.ndarray) -> float | None:
    """Unweighted RMS in dBFS over all channels and frames."""
    signal = np.asarray(signal, dtype=np.float64)
    if signal.size == 0:
        return None
    energy = float(np.mean(np.square(signal)))
    return db_from_linear(energy)


class _BiquadState:
    """Per-channel transposed direct form II biquad state (reference form).

    Used to synthesize the K-weighting impulse response from the BS.1770
    coefficients. The runtime path then applies that response through the
    FFT overlap-add convolver in :class:`_KWeightFilter`.
    """

    def __init__(self, channels: int):
        self.z1 = np.zeros(channels, dtype=np.float64)
        self.z2 = np.zeros(channels, dtype=np.float64)

    def process(self, x: np.ndarray, b: tuple, a: tuple) -> np.ndarray:
        b0, b1, b2 = b
        _, a1, a2 = a
        y = np.empty_like(x)
        z1, z2 = self.z1, self.z2
        for n in range(x.shape[0]):
            w = x[n] - a1 * z1 - a2 * z2
            y[n] = b0 * w + b1 * z1 + b2 * z2
            z2 = z1
            z1 = w
        self.z1, self.z2 = z1, z2
        return y


# Impulse-response length and a cache keyed by sample rate. The 4096-tap
# truncation leaves a residual below 2e-10 against the exact biquad cascade.
_IR_LENGTH = 4096
_IR_CACHE: dict[int, np.ndarray] = {}


def _kweight_impulse_response(sample_rate: int) -> np.ndarray:
    """Build (and cache) the K-weighting impulse response from the biquads.

    The two BS.1770 stages are applied to a unit impulse using the reference
    TD-II biquad, giving the exact cascaded impulse response truncated to
    :data:`_IR_LENGTH` taps. Sample-rate independent here, but cached per rate
    so future rate-specific stages need no call-site changes.
    """
    cached = _IR_CACHE.get(sample_rate)
    if cached is not None:
        return cached
    impulse = np.zeros((_IR_LENGTH, 1), dtype=np.float64)
    impulse[0, 0] = 1.0
    stage1 = _BiquadState(1)
    stage2 = _BiquadState(1)
    response = stage2.process(
        stage1.process(impulse, STAGE1_B, STAGE1_A), STAGE2_B, STAGE2_A
    )[:, 0]
    response.setflags(write=False)
    _IR_CACHE[sample_rate] = response
    return response


class _KWeightFilter:
    """Streaming K-weighting via FFT overlap-add convolution.

    State is the carry tail from the previous block, so processing a signal
    in any block sizes yields the same result as one in-memory pass. Only the
    carry (``_IR_LENGTH - 1`` frames) is retained between blocks.
    """

    def __init__(self, sample_rate: int):
        self._h = _kweight_impulse_response(sample_rate)
        # One independent carry tail per channel; lazily sized on first block.
        self._carries: dict[int, np.ndarray] = {}
        # Reuse the transformed IR for repeated blocks of the same length.
        self._spectrum_cache: dict[int, np.ndarray] = {}

    def _spectrum(self, nfft: int) -> np.ndarray:
        spectrum = self._spectrum_cache.get(nfft)
        if spectrum is None:
            spectrum = np.fft.rfft(self._h, nfft)
            self._spectrum_cache[nfft] = spectrum
        return spectrum

    def process_channel(self, x: np.ndarray, channel: int) -> np.ndarray:
        m = x.shape[0]
        # Size each transform to this block plus the IR tail so blocks of any
        # length (including one whole clip) are never truncated.
        nfft = 1 << (m + _IR_LENGTH - 2).bit_length()
        conv = np.fft.irfft(np.fft.rfft(x, nfft) * self._spectrum(nfft), nfft)[
            : m + _IR_LENGTH - 1
        ]
        carry = self._carries.get(channel)
        if carry is None:
            carry = np.zeros(_IR_LENGTH - 1, dtype=np.float64)
        # Every carry sample maps onto the start of this block's convolution.
        overlap = min(carry.shape[0], conv.shape[0])
        conv[:overlap] += carry[:overlap]
        out = conv[:m]
        self._carries[channel] = conv[m:]
        return out

    def process(self, block: np.ndarray) -> np.ndarray:
        return np.stack(
            [
                self.process_channel(block[:, c], c)
                for c in range(block.shape[1])
            ],
            axis=1,
        )


@dataclass
class LoudnessMeasurement:
    """Result of a streamed loudness measurement."""

    lufs_i: float | None
    rms_dbfs: float | None
    short_fallback: bool
    finite: bool


@dataclass
class LoudnessAccumulator:
    """Stream K-weighted block energies and finalize a gated LUFS value."""

    sample_rate: int
    _filter: _KWeightFilter | None = None
    _tail: np.ndarray | None = None  # weighted samples waiting for a full hop
    _total_frames: int = 0
    _square_sum: float = 0.0
    _block_energies: list[float] = field(default_factory=list)

    @property
    def window_frames(self) -> int:
        return int(round(WINDOW_MS / 1000.0 * self.sample_rate))

    @property
    def hop_frames(self) -> int:
        return int(round(HOP_MS / 1000.0 * self.sample_rate))

    def _weight(self, block: np.ndarray) -> np.ndarray:
        """Apply the K-weighting filter, allocating state on first use."""
        if self._filter is None:
            self._filter = _KWeightFilter(self.sample_rate)
        return self._filter.process(block)

    def add_block(self, block: np.ndarray) -> None:
        """Feed one ``(frames, channels)`` block into the measurement."""
        block = np.asarray(block, dtype=np.float64)
        if block.ndim == 1:
            block = block[:, None]
        if not np.all(np.isfinite(block)):
            raise FloatingPointError("non-finite sample in loudness input")
        self._square_sum += float(np.sum(np.square(block)))
        self._total_frames += block.shape[0]
        weighted = self._weight(block)
        if self._tail is None:
            self._tail = weighted
        else:
            self._tail = np.concatenate((self._tail, weighted), axis=0)
        window = self.window_frames
        hop = self.hop_frames
        while self._tail.shape[0] >= window:
            windowed = self._tail[:window]
            # Mean across channels, then frames: per-frame mean square.
            energy = float(np.mean(np.square(windowed)))
            self._block_energies.append(energy)
            self._tail = self._tail[hop:]

    def _gated_loudness(self) -> tuple[float | None, bool]:
        """Return (integrated LUFS, short-fallback flag) from block energies."""
        if self._total_frames == 0 or self._square_sum <= 0.0:
            return None, False
        if not self._block_energies:
            # Non-empty audio shorter than one full 400 ms window.
            whole = self._square_sum / (
                self._total_frames * (self._tail.shape[1] if self._tail is not None else 1)
            )
            return db_from_linear(whole), True

        energies = np.asarray(self._block_energies, dtype=np.float64)
        # Absolute gate: retain blocks louder than -70 LKFS.
        absolute_gated = energies[energies >= ABSOLUTE_GATE_ENERGY]
        if absolute_gated.size == 0:
            return None, False
        relative_threshold = float(np.mean(absolute_gated)) * (
            10.0 ** (RELATIVE_GATE_OFFSET_LU / 10.0)
        )
        # Relative gate: retain blocks 10 LU above the absolute-gated mean.
        final_gated = absolute_gated[absolute_gated >= relative_threshold]
        if final_gated.size == 0:
            return None, False
        return db_from_linear(float(np.mean(final_gated))), False

    def finalize(self) -> LoudnessMeasurement:
        lufs, fallback = self._gated_loudness()
        rms = (
            db_from_linear(
                self._square_sum / (self._total_frames * self._channels())
            )
            if self._total_frames
            else None
        )
        finite = lufs is not None and rms is not None and math.isfinite(rms)
        return LoudnessMeasurement(
            lufs_i=lufs,
            rms_dbfs=rms,
            short_fallback=fallback,
            finite=finite,
        )

    def _channels(self) -> int:
        return self._tail.shape[1] if self._tail is not None else 1


def measure_signal(signal: np.ndarray, sample_rate: int) -> LoudnessMeasurement:
    """Convenience wrapper measuring a complete in-memory signal."""
    signal = np.asarray(signal, dtype=np.float64)
    if signal.ndim == 1:
        signal = signal[:, None]
    acc = LoudnessAccumulator(sample_rate=sample_rate)
    acc.add_block(signal)
    return acc.finalize()
