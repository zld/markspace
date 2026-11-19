"""RMS, block K-weighted loudness, peak and clipping statistics.

Loudness follows the shape of ITU-R BS.1770: a high-shelf and a high-pass
pre-filter, mean-square power per 400 ms block with 75 % overlap, and
``-0.691 + 10*log10(power)`` in LUFS. Absolute and relative gating are applied
so silence reports as *undefined* rather than as NaN or -inf.

The biquads are implemented here with hand-written coefficient maths; no
library loudness meter is used.
"""

from __future__ import annotations

import math
from dataclasses import dataclass

import numpy as np

#: BS.1770 absolute gate in LUFS.
ABS_GATE_LUFS = -70.0
#: Relative gate below the ungated loudness.
REL_GATE_LU = -10.0
#: BS.1770 offset so a 1 kHz 0 dBFS sine reads about -3.01 LUFS.
LUFS_OFFSET = -0.691
#: Blocks shorter than this many samples still count as one block.
MIN_BLOCK_SAMPLES = 1


@dataclass(frozen=True)
class Biquad:
    b0: float
    b1: float
    b2: float
    a1: float
    a2: float


def _shelf_coeffs(rate: float) -> Biquad:
    """BS.1770 stage-1 high shelf (+4 dB above ~1.5 kHz).

    Uses the tan-prewarped shelf design from De Man's implementation notes so
    the 48 kHz coefficients reproduce the ones tabulated in BS.1770-4.
    """
    gain_db, q, fc = 3.999843853973347, 0.7071752369554196, 1681.974450955533
    k = math.tan(math.pi * fc / rate)
    vh = 10.0 ** (gain_db / 20.0)
    vb = vh ** 0.4996667741545416
    a0 = 1.0 + k / q + k * k
    b0 = (vh + vb * k / q + k * k) / a0
    b1 = 2.0 * (k * k - vh) / a0
    b2 = (vh - vb * k / q + k * k) / a0
    a1 = 2.0 * (k * k - 1.0) / a0
    a2 = (1.0 - k / q + k * k) / a0
    return Biquad(b0, b1, b2, a1, a2)


def _highpass_coeffs(rate: float) -> Biquad:
    """BS.1770 stage-2 high-pass (~38 Hz), Q = 0.5.

    BS.1770-4 tabulates the numerator as exactly (1, -2, 1) while the
    denominator is normalised, so the coefficients are reproduced that way
    rather than each divided by a0.
    """
    q, fc = 0.5003270373238773, 38.13547087602444
    w0 = 2.0 * math.pi * fc / rate
    alpha = math.sin(w0) / (2.0 * q)
    cos_w0 = math.cos(w0)
    a0 = 1.0 + alpha
    a1 = -2.0 * cos_w0
    a2 = 1.0 - alpha
    return Biquad(1.0, -2.0, 1.0, a1 / a0, a2 / a0)


def _apply_biquad(x: np.ndarray, coef: Biquad) -> np.ndarray:
    """Direct-form-I biquad along axis 0, per channel, zero initial state."""
    y = np.zeros_like(x, dtype=np.float64)
    z1 = np.zeros(x.shape[1], dtype=np.float64)
    z2 = np.zeros(x.shape[1], dtype=np.float64)
    b0, b1, b2, a1, a2 = coef.b0, coef.b1, coef.b2, coef.a1, coef.a2
    for n in range(x.shape[0]):
        xn = x[n]
        yn = b0 * xn + z1
        z1 = b1 * xn - a1 * yn + z2
        z2 = b2 * xn - a2 * yn
        y[n] = yn
    return y


def k_weight(data: np.ndarray, rate: int) -> np.ndarray:
    """Apply the two K-weighting stages to (frames, channels) audio."""
    if data.shape[0] == 0:
        return np.zeros_like(data, dtype=np.float64)
    shelf = _shelf_coeffs(float(rate))
    highpass = _highpass_coeffs(float(rate))
    return _apply_biquad(_apply_biquad(data.astype(np.float64), shelf), highpass)


def block_powers(frames: int, rate: int) -> list[tuple[int, int]]:
    """Block boundaries for gated loudness, 400 ms with 75 % overlap."""
    block = max(MIN_BLOCK_SAMPLES, int(round(0.400 * rate)))
    hop = max(1, block // 4)
    if frames <= 0:
        return []
    spans: list[tuple[int, int]] = []
    start = 0
    while start + block <= frames:
        spans.append((start, start + block))
        start += hop
    if not spans:
        spans.append((0, frames))
    elif spans[-1][1] < frames:
        # Final partial block runs to the last real frame; never padded with
        # zeros into the gating statistic.
        spans.append((frames - block if frames >= block else 0, frames))
    return spans


def _gate(powers: np.ndarray) -> float | None:
    """Two-stage gated mean power, or None if everything is below the gate."""
    if powers.size == 0:
        return None
    with np.errstate(divide="ignore"):
        lufs = LUFS_OFFSET + 10.0 * np.log10(powers)
    above = powers[lufs > ABS_GATE_LUFS]
    if above.size == 0:
        return None
    relative = LUFS_OFFSET + 10.0 * math.log10(float(np.mean(above))) + REL_GATE_LU
    kept = above[LUFS_OFFSET + 10.0 * np.log10(above) > relative]
    if kept.size == 0:
        kept = above
    return float(np.mean(kept))


@dataclass(frozen=True)
class LoudnessStats:
    rms: float
    rms_dbfs: float | None
    lufs: float | None
    peak_dbfs: float | None
    peak_abs: float
    clipped_samples: int
    silent: bool

    def as_dict(self) -> dict[str, float | None | int | bool]:
        return {
            "rms": _num(self.rms),
            "rms_dbfs": _num(self.rms_dbfs),
            "lufs": _num(self.lufs),
            "peak_dbfs": _num(self.peak_dbfs),
            "peak_abs": _num(self.peak_abs),
            "clipped_samples": int(self.clipped_samples),
            "silent": bool(self.silent),
        }


def _num(value: float | None) -> float | None:
    if value is None:
        return None
    value = float(value)
    return value if math.isfinite(value) else None


def _to_db(amplitude: float) -> float | None:
    if amplitude <= 0.0:
        return None
    return 20.0 * math.log10(amplitude)


def measure(data: np.ndarray, rate: int) -> LoudnessStats:
    """Compute all statistics for (frames, channels) float audio."""
    frames = int(data.shape[0])
    if data.ndim != 2:
        raise ValueError("audio must be (frames, channels)")
    if frames == 0:
        return LoudnessStats(0.0, None, None, None, 0.0, 0, True)
    peak_abs = float(np.max(np.abs(data))) if data.size else 0.0
    clipped = int(np.count_nonzero(np.abs(data) > 1.0))
    # RMS is the classic all-sample statistic and always lies in [0, peak].
    rms = math.sqrt(max(float(np.mean(np.square(data, dtype=np.float64))), 0.0))
    silent = peak_abs == 0.0
    lufs: float | None = None
    if not silent:
        weighted = k_weight(data, rate)
        spans = block_powers(frames, rate)
        # BS.1770 sums the (unity-weighted) mean square of every channel, which
        # is why a dual-mono file reads 3 dB louder than either channel alone.
        powers = np.array(
            [
                float(np.sum(np.mean(np.square(weighted[a:b], dtype=np.float64), axis=0)))
                for a, b in spans
            ],
            dtype=np.float64,
        )
        gated = _gate(powers)
        if gated is not None and gated > 0.0:
            lufs = LUFS_OFFSET + 10.0 * math.log10(gated)
    return LoudnessStats(
        rms=rms,
        rms_dbfs=_to_db(rms),
        lufs=lufs,
        peak_dbfs=_to_db(peak_abs),
        peak_abs=peak_abs,
        clipped_samples=clipped,
        silent=silent,
    )


@dataclass(frozen=True)
class GainDecision:
    gain_db: float
    limited_by_peak: bool
    reason: str


def normalisation_gain(
    stats: LoudnessStats,
    *,
    target_lufs: float,
    max_peak_dbfs: float | None,
) -> GainDecision:
    """Gain that hits target LUFS unless the peak ceiling forbids it."""
    if stats.lufs is None:
        return GainDecision(0.0, False, "silent_no_loudness")
    gain_db = target_lufs - stats.lufs
    reason = "target_lufs"
    limited = False
    if max_peak_dbfs is not None and stats.peak_abs > 0.0:
        headroom_db = max_peak_dbfs - 20.0 * math.log10(stats.peak_abs)
        if gain_db > headroom_db:
            gain_db = headroom_db
            limited = True
            reason = "peak_limited"
    if gain_db < -200.0:
        gain_db = -200.0
    return GainDecision(float(gain_db), limited, reason)


def gain_linear(gain_db: float) -> float:
    return float(10.0 ** (gain_db / 20.0))


def peak_limit_gain(stats: LoudnessStats, max_peak_dbfs: float) -> float | None:
    """Gain needed to bring the peak under the ceiling, or None if no limit."""
    if stats.peak_abs <= 0.0:
        return None
    headroom_db = max_peak_dbfs - 20.0 * math.log10(stats.peak_abs)
    if headroom_db >= 0.0:
        return None
    return float(10.0 ** (headroom_db / 20.0))
