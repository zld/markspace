"""Frame layout handling: interleaving, channel mapping, tail zero-padding.

soundfile returns frame-major (frames, channels) matrices. All helpers keep
that layout, preserve channel count and order (identity mapping), and provide
the tail zero-padding / strict cropping the resampling stage relies on.
"""
from __future__ import annotations

import numpy as np


def ensure_2d(data: np.ndarray) -> np.ndarray:
    """Normalize to (frames, channels); a mono 1-D vector becomes (n, 1)."""
    arr = np.asarray(data, dtype=np.float64)
    if arr.ndim == 1:
        arr = arr[:, None]
    if arr.ndim != 2:
        raise ValueError(f"expected 1-D or 2-D frame matrix, got {arr.ndim}-D")
    return arr


def channel_mapping(channels: int) -> tuple[int, ...]:
    """Identity channel map: count and order are preserved end to end."""
    if channels < 1:
        raise ValueError(f"channel count must be >= 1, got {channels}")
    return tuple(range(channels))


def deinterleave(frames: np.ndarray) -> list[np.ndarray]:
    """Split (frames, channels) into per-channel 1-D contiguous arrays."""
    frames = ensure_2d(frames)
    return [np.ascontiguousarray(frames[:, c]) for c in range(frames.shape[1])]


def interleave(channels: list[np.ndarray]) -> np.ndarray:
    """Reassemble per-channel 1-D arrays into a (frames, channels) matrix."""
    if not channels:
        raise ValueError("no channels to interleave")
    n = len(channels[0])
    if any(len(c) != n for c in channels):
        raise ValueError("channel length mismatch during interleave")
    return np.stack([np.asarray(c, dtype=np.float64) for c in channels], axis=1)


def pad_to_block(frames: np.ndarray, block: int) -> tuple[np.ndarray, int]:
    """Zero-pad the tail so the frame count is a multiple of `block`.

    Returns (padded_frames, n_padded). The padding participates in filtering;
    callers crop the output back to the exact target frame count afterwards.
    """
    frames = ensure_2d(frames)
    if block < 1:
        raise ValueError(f"block must be >= 1, got {block}")
    n = frames.shape[0]
    n_padded = (-n) % block
    if n_padded == 0:
        return frames, 0
    pad = np.zeros((n_padded, frames.shape[1]), dtype=frames.dtype)
    return np.concatenate([frames, pad], axis=0), n_padded


def crop_frames(frames: np.ndarray, n_frames: int) -> np.ndarray:
    """Strictly crop to exactly n_frames; refuses to invent frames."""
    frames = ensure_2d(frames)
    if n_frames < 0 or n_frames > frames.shape[0]:
        raise ValueError(
            f"cannot crop {frames.shape[0]} frames to {n_frames}")
    return np.ascontiguousarray(frames[:n_frames])
