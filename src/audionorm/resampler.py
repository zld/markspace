"""Self-contained multi-channel resampling kernel.

Windowed-sinc interpolation with an anti-aliasing low-pass whose cutoff
tracks min(rate_in, rate_out). Input is processed in fixed-size blocks;
the tail of a partial block is zero-padded for the computation and the
padding is trimmed from the output, so any input length (odd frame
counts included) round-trips to an exact, deterministic output length.

The same `resample` function backs both single-file and batch paths.
"""
from __future__ import annotations

import numpy as np

BLOCK_FRAMES = 4096   # processing block, in input frames
KERNEL_HALF = 32      # sinc taps on each side -> 64-tap kernel
CUTOFF_SCALE = 0.95   # fraction of the applicable Nyquist used as cutoff


def output_length(frames_in: int, rate_in: int, rate_out: int) -> int:
    """Exact output frame count for a conversion, by rate ratio."""
    if frames_in < 0:
        raise ValueError("frames_in must be >= 0")
    return int(round(frames_in * (rate_out / rate_in)))


def _kernel(offsets: np.ndarray, fc: float) -> np.ndarray:
    """Windowed-sinc weights for fractional offsets (in input samples)."""
    h = 2.0 * fc * np.sinc(2.0 * fc * offsets)
    x = np.clip(offsets / KERNEL_HALF, -1.0, 1.0)
    window = 0.42 + 0.5 * np.cos(np.pi * x) + 0.08 * np.cos(2.0 * np.pi * x)
    return h * window


def resample(samples: np.ndarray, rate_in: int, rate_out: int,
             block: int = BLOCK_FRAMES) -> np.ndarray:
    """Resample (frames, channels) float audio to ``rate_out``.

    Returns an array of exactly ``output_length(frames_in, ...)`` frames.
    Channels are filtered independently from the same interleaved input;
    no cross-channel mixing occurs.
    """
    if rate_in <= 0 or rate_out <= 0:
        raise ValueError("sample rates must be positive")
    x = np.asarray(samples, dtype=np.float64)
    if x.ndim == 1:
        x = x[:, None]
    n_in, channels = x.shape
    n_out = output_length(n_in, rate_in, rate_out)
    if n_out == 0:
        return np.zeros((0, channels), dtype=np.float64)
    if n_in == 0:
        return np.zeros((n_out, channels), dtype=np.float64)

    ratio = rate_in / rate_out  # input samples advanced per output sample
    fc = 0.5 * min(1.0, 1.0 / ratio) * CUTOFF_SCALE

    # Zero-pad to a whole number of blocks, plus kernel wings on both ends.
    tail_pad = (-n_in) % block
    padded = np.pad(x, ((KERNEL_HALF, KERNEL_HALF + tail_pad), (0, 0)))

    taps = np.arange(-KERNEL_HALF + 1, KERNEL_HALF + 1)  # 64 tap offsets
    out = np.empty((n_out, channels), dtype=np.float64)
    step = max(1, int(block / ratio))  # output frames produced per iteration

    for start in range(0, n_out, step):
        m = np.arange(start, min(start + step, n_out))
        t = m * ratio
        base = np.floor(t).astype(np.int64)
        idx = base[:, None] + taps[None, :]          # (M, 64) input positions
        offsets = t[:, None] - idx
        weights = _kernel(offsets, fc)               # (M, 64)
        gathered = padded[idx + KERNEL_HALF]         # (M, 64, C) zero-pad safe
        out[start:start + len(m)] = np.einsum("mt,mtc->mc", weights, gathered)

    return out


def estimate_true_peak(samples: np.ndarray, rate: int,
                       oversample: int = 4) -> float:
    """Approximate true peak (dBTP-style) via 4x oversampling with this kernel."""
    x = np.asarray(samples, dtype=np.float64)
    if x.size == 0:
        return 0.0
    up = resample(x, rate, rate * oversample)
    return float(np.max(np.abs(up))) if up.size else 0.0
