"""Level statistics: RMS, K-weighted approximate LUFS, and gain math.

LUFS here is an explicit approximation: the signal is resampled to
48 kHz with the project resampling kernel, filtered with the two
BS.1770 K-weighting biquad stages (pre-filter shelf + RLB high-pass,
48 kHz coefficients), and the ungated channel-sum mean square is
converted with the -0.691 offset. It is NOT a substitute for a full
broadcast-standard (gated) loudness measurement.

Silence convention: any statistic whose energy is exactly zero is
reported as None (JSON null) and the file is flagged silent.
"""
from __future__ import annotations

import math

import numpy as np

from .resampler import resample

MEASUREMENT_RATE = 48000
LUFS_OFFSET = -0.691
SILENCE_FLOOR = 1e-12  # linear RMS below this is treated as silence

# BS.1770 K-weighting coefficients for 48 kHz.
_STAGE1_B = (1.53512485958697, -2.69169618940638, 1.19839281085285)
_STAGE1_A = (1.0, -1.69065929318241, 0.73248077421585)
_STAGE2_B = (1.0, -2.0, 1.0)
_STAGE2_A = (1.0, -1.99004745483398, 0.99007225036621)


def _biquad(x: np.ndarray, b: tuple, a: tuple) -> np.ndarray:
    """Direct-form-II biquad, applied per channel (column)."""
    y = np.empty_like(x)
    for c in range(x.shape[1]):
        x1 = x2 = y1 = y2 = 0.0
        col = x[:, c]
        out = y[:, c]
        for n in range(col.shape[0]):
            xn = col[n]
            yn = b[0] * xn + b[1] * x1 + b[2] * x2 - a[1] * y1 - a[2] * y2
            out[n] = yn
            x2, x1 = x1, xn
            y2, y1 = y1, yn
    return y


def _as_2d(samples: np.ndarray) -> np.ndarray:
    x = np.asarray(samples, dtype=np.float64)
    return x[:, None] if x.ndim == 1 else x


def rms_db(samples: np.ndarray) -> tuple[list[float | None], float | None]:
    """Per-channel and overall RMS in dBFS; None where silent."""
    x = _as_2d(samples)
    if x.shape[0] == 0:
        return [None] * x.shape[1], None
    per_ch = []
    for c in range(x.shape[1]):
        r = float(np.sqrt(np.mean(np.square(x[:, c]))))
        per_ch.append(20.0 * math.log10(r) if r >= SILENCE_FLOOR else None)
    r_all = float(np.sqrt(np.mean(np.square(x))))
    overall = 20.0 * math.log10(r_all) if r_all >= SILENCE_FLOOR else None
    return per_ch, overall


def lufs(samples: np.ndarray, rate: int) -> float | None:
    """Ungated K-weighted approximate loudness; None for silence."""
    x = _as_2d(samples)
    if x.shape[0] == 0:
        return None
    if rate != MEASUREMENT_RATE:
        x = resample(x, rate, MEASUREMENT_RATE)
    weighted = _biquad(_biquad(x, _STAGE1_B, _STAGE1_A), _STAGE2_B, _STAGE2_A)
    mean_square = float(np.sum(np.mean(np.square(weighted), axis=0)))
    if mean_square <= 0.0:
        return None
    return LUFS_OFFSET + 10.0 * math.log10(mean_square)


def count_clipped(samples: np.ndarray) -> int:
    """Samples at or beyond full scale in the float domain."""
    x = np.asarray(samples, dtype=np.float64)
    return int(np.count_nonzero(np.abs(x) >= 1.0))


def gain_db_for_target(measured_lufs: float | None, target_lufs: float) -> float:
    """Gain needed to hit the target; 0 dB for silence (no gain applied)."""
    if measured_lufs is None:
        return 0.0
    return target_lufs - measured_lufs


def limit_gain_for_peak(samples: np.ndarray, rate: int, gain_db: float,
                        ceiling: float, true_peak_fn) -> tuple[float, bool]:
    """Clamp gain so the post-gain true peak stays under ``ceiling`` (linear).

    Returns (possibly reduced gain_db, was_limited). The clamp applies in
    both directions: a boost is reduced, and if the ungained signal already
    exceeds the ceiling the gain is pulled below 0 dB.
    """
    peak = true_peak_fn(samples, rate)
    if peak <= 0.0:
        return gain_db, False
    max_gain = 20.0 * math.log10(ceiling / peak)
    if gain_db > max_gain:
        return max_gain, True
    return gain_db, False
