"""Loudness, peak and clipping measurements plus gain decisions.

Measurement
-----------
:class:`LevelMeter` is a streaming meter fed with resampled audio.  It
tracks per channel sums of squares, the absolute peak, samples beyond full
scale (``|x| > 1``) and the K-weighted mean square.  K-weighting is the
ITU-R BS.1770 curve recreated from its published analog prototype with a
bilinear transform, so it is valid at every sample rate (the published
coefficient tables only cover 48 kHz):

* stage 1 -- high shelf, ``f0 = 1681.974450955533 Hz``, ``G = +4 dB``,
  ``Q = 0.7071752369554196``;
* stage 2 -- high pass, ``f0 = 38.13547087602444 Hz``,
  ``Q = 0.5003270373238773``.

The two biquads are fused into one per-sample loop and carried across
chunks with four state variables, so chunk boundaries do not change the
result.  Loudness is :func:`lufs_from_energy` applied to the K-weighted
mean square: ``-0.691 + 10 log10(ms)``.  This is the documented
"K-weighted loudness approximation": it is the same ungated formula used
by common meters before the relative gate is applied, and it is used for
both the before and the after measurement so the two are comparable.

Gain planning
-------------
:func:`decide_gain` is a pure function: target gain from LUFS, capped by
an optional maximum gain, then reduced so the predicted peak does not
exceed a ceiling.  It reports the final gain and *why* it was limited.
"""

from __future__ import annotations

import math
from dataclasses import dataclass
from typing import Optional

import numpy as np

__all__ = [
    "CLIP_THRESHOLD",
    "LevelMeter",
    "Measurement",
    "analyse_blocks",
    "decide_gain",
    "k_weighting_coefficients",
    "lufs_from_energy",
]

# Fixed-point PCM cannot represent samples outside [-1, 1], so anything
# with a larger magnitude is a clipped sample.
CLIP_THRESHOLD = 1.0
# Headroom kept when planning gain, covering the final quantisation step.
PEAK_CEILING = 0.999
_MIN_ENERGY = 1e-30

# BS.1770 analog prototype parameters.
_SHELF_F0 = 1681.974450955533
_SHELF_GAIN_DB = 4.0
_SHELF_Q = 0.7071752369554196
_HIGHPASS_F0 = 38.13547087602444
_HIGHPASS_Q = 0.5003270373238773
# Pre-roll zeros flushed through the filter so both before/after runs and
# any verification re-run start from the same defined state.
_KFILTER_PREROLL = 512


def _shelf_coefficients(sample_rate: int) -> tuple[np.ndarray, np.ndarray]:
    k = math.tan(math.pi * _SHELF_F0 / sample_rate)
    vh = 10.0 ** (_SHELF_GAIN_DB / 20.0)
    vb = vh ** 0.4996667741545416
    a0 = 1.0 + k / _SHELF_Q + k * k
    b = np.array(
        [
            (vh + vb * k / _SHELF_Q + k * k) / a0,
            2.0 * (k * k - vh) / a0,
            (vh - vb * k / _SHELF_Q + k * k) / a0,
        ],
        dtype=np.float64,
    )
    a = np.array(
        [1.0, 2.0 * (k * k - 1.0) / a0, (1.0 - k / _SHELF_Q + k * k) / a0],
        dtype=np.float64,
    )
    return b, a


def _highpass_coefficients(sample_rate: int) -> tuple[np.ndarray, np.ndarray]:
    k = math.tan(math.pi * _HIGHPASS_F0 / sample_rate)
    a0 = 1.0 + k / _HIGHPASS_Q + k * k
    b = np.array([1.0 / a0, -2.0 / a0, 1.0 / a0], dtype=np.float64)
    a = np.array(
        [1.0, 2.0 * (k * k - 1.0) / a0, (1.0 - k / _HIGHPASS_Q + k * k) / a0],
        dtype=np.float64,
    )
    return b, a


def k_weighting_coefficients(sample_rate: int) -> tuple[np.ndarray, np.ndarray]:
    """Return ``(b, a)`` for both stages, each of shape ``(2, 3)``."""
    if not isinstance(sample_rate, int) or isinstance(sample_rate, bool) or sample_rate <= 0:
        raise ValueError(f"sample_rate must be a positive integer, got {sample_rate!r}")
    sb, sa = _shelf_coefficients(sample_rate)
    hb, ha = _highpass_coefficients(sample_rate)
    return np.stack([sb, hb]), np.stack([sa, ha])


def lufs_from_energy(mean_square: float) -> Optional[float]:
    """Convert K-weighted mean-square energy to LUFS (``None`` for silence)."""
    if not math.isfinite(mean_square) or mean_square <= _MIN_ENERGY:
        return None
    return -0.691 + 10.0 * math.log10(mean_square)


def _kfilter_chunk(
    x: np.ndarray, coeffs: tuple[np.ndarray, np.ndarray], state: np.ndarray
) -> tuple[np.ndarray, np.ndarray]:
    """Run the two-stage K-weighting cascade over one channel chunk.

    ``state`` holds ``[shelf_z1, shelf_z2, hp_z1, hp_z2]`` and is updated
    in place.  Returns the filtered signal.
    """
    b, a = coeffs
    sb0, sb1, sb2 = float(b[0, 0]), float(b[0, 1]), float(b[0, 2])
    sa1, sa2 = float(a[0, 1]), float(a[0, 2])
    hb0, hb1, hb2 = float(b[1, 0]), float(b[1, 1]), float(b[1, 2])
    ha1, ha2 = float(a[1, 1]), float(a[1, 2])
    z1 = float(state[0])
    z2 = float(state[1])
    w1 = float(state[2])
    w2 = float(state[3])
    out = [0.0] * x.shape[0]
    for i, xi in enumerate(x.tolist()):
        # Stage 1: high shelf.
        v = sb0 * xi + z1
        z1 = sb1 * xi - sa1 * v + z2
        z2 = sb2 * xi - sa2 * v
        # Stage 2: high pass.
        w = hb0 * v + w1
        w1 = hb1 * v - ha1 * w + w2
        w2 = hb2 * v - ha2 * w
        out[i] = w
    state[0] = z1
    state[1] = z2
    state[2] = w1
    state[3] = w2
    return np.asarray(out, dtype=np.float64), state


@dataclass
class Measurement:
    """One snapshot of the objective measurements."""

    channels: int
    frames: int
    sample_rate: int
    rms_per_channel: list[float]
    rms: float
    peak: float
    clipped_samples: int
    lufs: Optional[float]
    k_weighted_mean_square: float

    def to_dict(self) -> dict:
        return {
            "frames": self.frames,
            "channels": self.channels,
            "sample_rate": self.sample_rate,
            "rms_per_channel": list(self.rms_per_channel),
            "rms": self.rms,
            "peak": self.peak,
            "clipped_samples": self.clipped_samples,
            "lufs": self.lufs,
        }


class LevelMeter:
    """Streaming objective meter: RMS, peak, clipping, K-weighted LUFS."""

    def __init__(self, sample_rate: int, channels: int, *, preroll: int = _KFILTER_PREROLL) -> None:
        if not isinstance(sample_rate, int) or isinstance(sample_rate, bool) or sample_rate <= 0:
            raise ValueError(f"sample_rate must be a positive integer, got {sample_rate!r}")
        if not isinstance(channels, int) or isinstance(channels, bool) or not 1 <= channels <= 8:
            raise ValueError(f"channels must be an integer between 1 and 8, got {channels!r}")
        self.sample_rate = sample_rate
        self.channels = channels
        self.frames = 0
        self._sum_sq = np.zeros(channels, dtype=np.float64)
        self._peak = 0.0
        self._clipped = 0
        self._k_sum_sq = 0.0
        self._k_frames = 0
        self._coeffs = k_weighting_coefficients(sample_rate)
        self._state = np.zeros((channels, 4), dtype=np.float64)
        if preroll > 0:
            # Prime the filter state with silence, but do not count those
            # samples in the loudness denominator: only real programme
            # frames contribute to the mean square.
            for ch in range(channels):
                _kfilter_chunk(
                    np.zeros((preroll,), dtype=np.float64),
                    self._coeffs,
                    self._state[ch],
                )

    def _feed_kfilter(self, block: np.ndarray) -> None:
        for ch in range(self.channels):
            filtered, _ = _kfilter_chunk(block[:, ch], self._coeffs, self._state[ch])
            self._k_sum_sq += float(np.dot(filtered, filtered))
            self._k_frames += filtered.shape[0]

    def feed(self, block: np.ndarray) -> None:
        arr = np.asarray(block, dtype=np.float64)
        if arr.ndim != 2 or arr.shape[1] != self.channels:
            raise ValueError(
                f"expected frames x {self.channels} array, got shape {arr.shape}"
            )
        if arr.shape[0] == 0:
            return
        if not np.all(np.isfinite(arr)):
            raise ValueError("input contains non-finite samples")
        self.frames += arr.shape[0]
        self._sum_sq += np.einsum("ij,ij->j", arr, arr)
        self._peak = max(self._peak, float(np.max(np.abs(arr))))
        self._clipped += int(np.count_nonzero(np.abs(arr) > CLIP_THRESHOLD))
        self._feed_kfilter(arr)

    def snapshot(self) -> Measurement:
        if self.frames:
            per_channel = self._sum_sq / self.frames
            rms = float(np.sqrt(np.mean(per_channel)))
        else:
            per_channel = np.zeros(self.channels)
            rms = 0.0
        mean_square = self._k_sum_sq / self._k_frames if self._k_frames else 0.0
        return Measurement(
            channels=self.channels,
            frames=self.frames,
            sample_rate=self.sample_rate,
            rms_per_channel=[float(v) for v in np.sqrt(per_channel)],
            rms=rms,
            peak=float(self._peak),
            clipped_samples=int(self._clipped),
            lufs=lufs_from_energy(mean_square),
            k_weighted_mean_square=float(mean_square),
        )
