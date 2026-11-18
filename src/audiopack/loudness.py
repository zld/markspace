"""Block RMS, K-weighted gated loudness (BS.1770-style), and gain staging.

K-weighting uses the standard two-stage filter (high shelf + RLB high-pass)
realized as RBJ biquads, applied by chunked FFT convolution so memory stays
bounded on long material. Integrated loudness uses 400 ms blocks with 75%
overlap, an absolute gate at -70 LUFS and a relative gate at -10 LU.
"""

from __future__ import annotations

import math

import numpy as np

from audiopack.resample import ResamplerKernel

ABSOLUTE_GATE_LUFS = -70.0
RELATIVE_GATE_LU = -10.0
LOUDNESS_OFFSET = -0.691
BLOCK_SECONDS = 0.4
BLOCK_OVERLAP = 0.75
_FILTER_CHUNK = 1 << 20


def _biquad_high_shelf(rate: int) -> tuple[np.ndarray, np.ndarray]:
    f0, gain_db, q = 1681.9744509555319, 3.99984385397, 0.7071752369554193
    A = 10.0 ** (gain_db / 40.0)
    w0 = 2.0 * math.pi * f0 / rate
    alpha = math.sin(w0) / (2.0 * q)
    c = math.cos(w0)
    sa = 2.0 * math.sqrt(A) * alpha
    b = np.array([A * ((A + 1) + (A - 1) * c + sa),
                  -2 * A * ((A - 1) + (A + 1) * c),
                  A * ((A + 1) + (A - 1) * c - sa)])
    a = np.array([(A + 1) - (A - 1) * c + sa,
                  2 * ((A - 1) - (A + 1) * c),
                  (A + 1) - (A - 1) * c - sa])
    return b / a[0], a / a[0]


def _biquad_high_pass(rate: int) -> tuple[np.ndarray, np.ndarray]:
    f0, q = 38.13547087613982, 0.5003270373253953
    w0 = 2.0 * math.pi * f0 / rate
    alpha = math.sin(w0) / (2.0 * q)
    c = math.cos(w0)
    b = np.array([(1 + c) / 2, -(1 + c), (1 + c) / 2])
    a = np.array([1 + alpha, -2 * c, 1 - alpha])
    return b / a[0], a / a[0]


def _combined_response(rate: int, nfft: int) -> np.ndarray:
    z1 = np.exp(-2j * np.pi * np.arange(nfft // 2 + 1) / nfft)
    z2 = z1 * z1
    h = np.ones(nfft // 2 + 1, dtype=np.complex128)
    for b, a in (_biquad_high_shelf(rate), _biquad_high_pass(rate)):
        h *= (b[0] + b[1] * z1 + b[2] * z2) / (a[0] + a[1] * z1 + a[2] * z2)
    return h


def k_weighted(x: np.ndarray, rate: int) -> np.ndarray:
    """Apply the K-weighting filter chain to a mono signal (chunked)."""
    x = np.ascontiguousarray(x, dtype=np.float64)
    n = x.shape[0]
    if n == 0:
        return x
    margin = min(int(0.1 * rate), max(0, n - 1))
    step = _FILTER_CHUNK
    out = np.empty(n, dtype=np.float64)
    pos = 0
    while pos < n:
        lo = max(0, pos - margin)
        hi = min(n, pos + step + margin)
        seg = x[lo:hi]
        nfft = 1 << (2 * seg.shape[0] - 1).bit_length()
        spec = np.fft.rfft(seg, nfft) * _combined_response(rate, nfft)
        y = np.fft.irfft(spec, nfft)[: seg.shape[0]]
        keep = min(step, n - pos)
        out[pos:pos + keep] = y[pos - lo: pos - lo + keep]
        pos += keep
    return out


def block_mean_squares(x: np.ndarray, rate: int) -> np.ndarray:
    """Mean square per 400 ms block with 75% overlap (mono signal)."""
    block = max(1, int(round(BLOCK_SECONDS * rate)))
    hop = max(1, int(round(block * (1.0 - BLOCK_OVERLAP))))
    n = x.shape[0]
    if n < block:
        return np.array([float(np.mean(x * x))]) if n else np.zeros(0)
    starts = np.arange(0, n - block + 1, hop)
    csum = np.concatenate(([0.0], np.cumsum(x * x)))
    return (csum[starts + block] - csum[starts]) / block


def integrated_loudness(data: np.ndarray, rate: int) -> float:
    """Gated K-weighted loudness in LUFS; -inf for (near-)silence."""
    if data.size == 0:
        return -math.inf
    z = np.zeros(0)
    for ch in range(data.shape[1]):
        ms = block_mean_squares(k_weighted(data[:, ch], rate), rate)
        z = ms if z.size == 0 else z + ms
    if z.size == 0:
        return -math.inf
    loud = LOUDNESS_OFFSET + 10.0 * np.log10(np.maximum(z, 1e-30))
    keep = loud > ABSOLUTE_GATE_LUFS
    if not keep.any():
        return -math.inf
    z_g = z[keep]
    rel_threshold = LOUDNESS_OFFSET + 10.0 * math.log10(float(np.mean(z_g))) + RELATIVE_GATE_LU
    z_final = z_g[loud[keep] > rel_threshold]
    if z_final.size == 0:
        return -math.inf
    return LOUDNESS_OFFSET + 10.0 * math.log10(float(np.mean(z_final)))


def rms_dbfs(data: np.ndarray, rate: int) -> float:
    """Overall RMS in dBFS, accumulated over fixed-size blocks."""
    block = max(1, int(round(BLOCK_SECONDS * rate)))
    total, count = 0.0, 0
    for start in range(0, data.shape[0], block):
        seg = data[start:start + block]
        total += float(np.sum(seg.astype(np.float64) ** 2))
        count += seg.size
    if count == 0 or total <= 0.0:
        return -math.inf
    return 10.0 * math.log10(total / count)


def sample_peak_dbfs(data: np.ndarray) -> float:
    peak = float(np.max(np.abs(data))) if data.size else 0.0
    return 20.0 * math.log10(peak) if peak > 0.0 else -math.inf


def true_peak_dbfs(data: np.ndarray, rate: int, oversample: int = 4) -> float:
    """True-peak estimate via polyphase oversampling, computed in chunks."""
    kernel = ResamplerKernel(rate, rate * oversample)
    margin = 2 * kernel.half
    chunk = 1 << 20
    peak = 0.0
    for ch in range(data.shape[1]):
        x = data[:, ch]
        pos = 0
        while pos < x.shape[0]:
            lo = max(0, pos - margin)
            hi = min(x.shape[0], pos + chunk + margin)
            y = kernel.resample_channel(x[lo:hi])
            skip = (pos - lo) * oversample
            keep = min(chunk, x.shape[0] - pos) * oversample
            seg = y[skip:skip + keep]
            if seg.size:
                peak = max(peak, float(np.max(np.abs(seg))))
            pos += chunk
    return 20.0 * math.log10(peak) if peak > 0.0 else -math.inf


def count_clipped(data: np.ndarray, limit: float = 1.0) -> int:
    """Number of samples at or beyond the clipping limit."""
    if data.size == 0:
        return 0
    return int(np.count_nonzero(np.abs(data) >= limit))


def normalization_gain_db(
    lufs: float, target_lufs: float, peak_dbfs: float, ceiling_dbfs: float
) -> float:
    """Gain toward the loudness target, capped by the peak ceiling.

    Silence (non-finite loudness) is never amplified. The gain is reduced when
    the sample/true peak would otherwise exceed ``ceiling_dbfs``.
    """
    if not math.isfinite(lufs):
        return 0.0
    gain = target_lufs - lufs
    if math.isfinite(peak_dbfs):
        gain = min(gain, ceiling_dbfs - peak_dbfs)
    return gain
