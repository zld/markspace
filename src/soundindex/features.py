"""Frame energy envelope, robust noise-floor estimation and smoothing."""
from __future__ import annotations

import numpy as np

_EPS = 1e-12


def frame_envelope_db(chunks, frame: int, hop: int) -> np.ndarray:
    """Per-frame max-channel RMS energy in dB, from sample chunks.

    Multi-channel input is reduced by taking the maximum channel energy
    per frame, so all channels share one sample-boundary timeline.
    """
    buf = None
    out = []
    for chunk in chunks:
        buf = chunk if buf is None else np.concatenate([buf, chunk], axis=0)
        n_frames = (len(buf) - frame) // hop + 1 if len(buf) >= frame else 0
        if n_frames <= 0:
            continue
        span = (n_frames - 1) * hop + frame
        view = np.lib.stride_tricks.sliding_window_view(buf[:span], frame, axis=0)
        windows = view[::hop]  # (n_frames, channels, frame)
        energy = np.square(windows, dtype=np.float64).mean(axis=-1).max(axis=-1)
        out.append(energy)
        buf = buf[n_frames * hop:]
    if not out:
        return np.zeros(0, dtype=np.float64)
    return 10.0 * np.log10(np.concatenate(out) + _EPS)


def smooth_db(env_db: np.ndarray, win: int = 3) -> np.ndarray:
    """Short moving-average smoothing with edge padding."""
    if win <= 1 or env_db.size == 0:
        return env_db
    kernel = np.ones(win, dtype=np.float64) / win
    padded = np.pad(env_db, win // 2, mode="edge")
    return np.convolve(padded, kernel, mode="valid")[: env_db.size]


def noise_floor_db(env_db: np.ndarray, window: int, percentile: float = 10.0) -> np.ndarray:
    """Rolling low-percentile noise floor estimate, computed in blocks.

    A low percentile over a sliding window tracks the room/AC noise base
    without being pulled up by loud programme material.
    """
    n = env_db.size
    if n == 0:
        return env_db.copy()
    window = max(1, int(window) | 1)
    half = window // 2
    padded = np.pad(env_db, half, mode="edge")
    out = np.empty(n, dtype=np.float64)
    block = 4096
    for lo in range(0, n, block):
        hi = min(lo + block, n)
        windows = np.lib.stride_tricks.sliding_window_view(
            padded[lo:hi + 2 * half], window)
        out[lo:hi] = np.percentile(windows, percentile, axis=1)
    return out
