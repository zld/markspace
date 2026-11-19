"""Loudness measurement and normalization gain staging.

RMS, an approximate ITU-R BS.1770 K-weighted LUFS (standard biquad constants
with bilinear-transform redesign at the working rate, no gating), a true-peak
estimate from 4x oversampling with the project's own resampling kernel, and
clipping counts. All quantities are measured on target-rate data; silence is
reported as None (minus infinity) and marked non-normalizable.
"""
from __future__ import annotations

import math

import numpy as np

from . import layout, resample

LOUDNESS_OFFSET = -0.691      # BS.1770 absolute offset
OVERSAMPLE_FACTOR = 4         # true-peak oversampling
PEAK_CEILING_DB = -0.3        # true-peak ceiling for normalization backoff

# ITU-R BS.1770 K-weighting analog prototype constants.
_SHELF_FC, _SHELF_GAIN_DB, _SHELF_Q = 1681.974450955533, 3.999843853973347, 0.7071752369554196
_RLB_FC, _RLB_Q = 38.13547087602444, 0.5003270373238773

_TRUEPEAK_PLANS: dict[int, resample.ResamplePlan] = {}


def _high_shelf(rate: int) -> tuple[np.ndarray, np.ndarray]:
    k = math.tan(math.pi * _SHELF_FC / rate)
    vh = 10.0 ** (_SHELF_GAIN_DB / 20.0)
    vb = vh ** 0.4996667741545416
    a0 = 1.0 + k / _SHELF_Q + k * k
    b = np.array([vh + vb * k / _SHELF_Q + k * k,
                  2.0 * (k * k - vh),
                  vh - vb * k / _SHELF_Q + k * k]) / a0
    a = np.array([1.0, 2.0 * (k * k - 1.0) / a0,
                  (1.0 - k / _SHELF_Q + k * k) / a0])
    return b, a


def _high_pass(rate: int) -> tuple[np.ndarray, np.ndarray]:
    k = math.tan(math.pi * _RLB_FC / rate)
    a0 = 1.0 + k / _RLB_Q + k * k
    b = np.array([1.0, -2.0, 1.0]) / a0
    a = np.array([1.0, 2.0 * (k * k - 1.0) / a0,
                  (1.0 - k / _RLB_Q + k * k) / a0])
    return b, a


def _apply_biquad(frames: np.ndarray, b: np.ndarray, a: np.ndarray) -> np.ndarray:
    """Direct-form-II-transposed biquad, zero initial state, per channel.

    Stable for signals shorter than the filter's impulse response; the
    measured frame count is reported by the caller.
    """
    b0, b1, b2 = float(b[0]), float(b[1]), float(b[2])
    a1, a2 = float(a[1]), float(a[2])
    y = np.empty_like(frames)
    for c in range(frames.shape[1]):
        col = frames[:, c]
        out = y[:, c]
        s1 = s2 = 0.0
        for i in range(col.shape[0]):
            xn = float(col[i])
            yn = b0 * xn + s1
            s1 = b1 * xn - a1 * yn + s2
            s2 = b2 * xn - a2 * yn
            out[i] = yn
    return y


def k_weight(frames: np.ndarray, rate: int) -> np.ndarray:
    """Apply the two-stage K-weighting filter to a (frames, channels) matrix."""
    frames = layout.ensure_2d(frames)
    b, a = _high_shelf(rate)
    out = _apply_biquad(frames, b, a)
    b, a = _high_pass(rate)
    return _apply_biquad(out, b, a)


def rms_dbfs(frames: np.ndarray) -> float | None:
    """Wideband RMS over all channels in dBFS; None for digital silence."""
    frames = layout.ensure_2d(frames)
    if frames.size == 0:
        return None
    ms = float(np.mean(np.square(frames)))
    if ms <= 0.0:
        return None
    return 10.0 * math.log10(ms)


def lufs(frames: np.ndarray, rate: int) -> float | None:
    """Approximate K-weighted loudness; None for digital silence."""
    weighted = k_weight(frames, rate)
    total = float(np.mean(np.square(weighted), axis=0).sum())
    if total <= 0.0:
        return None
    return LOUDNESS_OFFSET + 10.0 * math.log10(total)


def true_peak_dbtp(frames: np.ndarray, rate: int) -> float | None:
    """True-peak estimate via 4x oversampling with the resample kernel."""
    frames = layout.ensure_2d(frames)
    if frames.shape[0] == 0:
        return None
    plan = _TRUEPEAK_PLANS.get(rate)
    if plan is None:
        plan = resample.plan_resample(rate, rate * OVERSAMPLE_FACTOR)
        _TRUEPEAK_PLANS[rate] = plan
    peak = float(np.abs(frames).max())
    for col in layout.deinterleave(frames):
        over = resample.apply_plan(plan, col)
        if over.size:
            peak = max(peak, float(np.abs(over).max()))
    if peak <= 0.0:
        return None
    return 20.0 * math.log10(peak)


def clipped_samples(frames: np.ndarray) -> int:
    """Count samples at or beyond full scale (|x| >= 1.0)."""
    frames = layout.ensure_2d(frames)
    return int(np.count_nonzero(np.abs(frames) >= 1.0))


def measure(frames: np.ndarray, rate: int) -> dict:
    """All loudness metrics for one target-rate buffer."""
    frames = layout.ensure_2d(frames)
    return {
        "rms_dbfs": rms_dbfs(frames),
        "lufs": lufs(frames, rate),
        "true_peak_dbtp": true_peak_dbtp(frames, rate),
        "clipped_samples": clipped_samples(frames),
        "measured_frames": int(frames.shape[0]),
    }


def normalization_gain(lufs_in: float | None, true_peak_dbtp_in: float | None,
                       target_lufs: float, max_boost_db: float,
                       ceiling_db: float = PEAK_CEILING_DB) -> tuple[float, bool]:
    """Gain in dB to reach target_lufs, plus a normalizable flag.

    Boost is capped at max_boost_db; if the estimated post-gain true peak
    would exceed ceiling_db the gain is backed off (attenuation allowed).
    Silence stays silent and is flagged non-normalizable.
    """
    if lufs_in is None:
        return 0.0, False
    gain = min(float(target_lufs) - lufs_in, float(max_boost_db))
    if true_peak_dbtp_in is not None and true_peak_dbtp_in + gain > ceiling_db:
        gain = ceiling_db - true_peak_dbtp_in
    return gain, True
