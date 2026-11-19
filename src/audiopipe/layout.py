"""Interleaved frame layout, channel mapping and tail-block handling."""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np

from .errors import InputError


@dataclass(frozen=True)
class Chunk:
    """One frame-aligned slice of an audio array.

    ``data`` is (frames, channels) float64. ``valid_frames`` is the number of
    frames that belong to the real signal; padded frames at the tail are zeros
    and are never part of ``valid_frames``.
    """

    data: np.ndarray
    valid_frames: int

    @property
    def padded_frames(self) -> int:
        return int(self.data.shape[0]) - self.valid_frames


def map_channels(data: np.ndarray, mode: str) -> np.ndarray:
    """Apply an explicit channel policy, never dropping channels silently."""
    if data.ndim != 2:
        raise InputError("audio must be (frames, channels)", reason="bad_layout")
    if mode == "preserve":
        return data
    if mode == "mono":
        if data.shape[1] == 1:
            return data
        # Energy-preserving average; shape stays 2-D so callers are uniform.
        return data.mean(axis=1, keepdims=True, dtype=np.float64)
    if mode.startswith("exact:") or mode.isdigit():
        wanted = int(mode.split(":", 1)[1]) if mode.startswith("exact:") else int(mode)
        if data.shape[1] != wanted:
            raise InputError(
                f"file has {data.shape[1]} channel(s), {wanted} required",
                reason="channel_mismatch",
            )
        return data
    raise InputError(f"unknown channel mode {mode!r}", reason="bad_channels")


def channel_note(channels_in: int, mode: str) -> str:
    """Human readable description of the channel mapping for reports."""
    if mode == "preserve":
        return f"preserved {channels_in} channel(s)"
    if mode == "mono":
        return f"averaged {channels_in} channel(s) to mono"
    wanted = int(mode.split(":", 1)[1]) if mode.startswith("exact:") else int(mode)
    return f"kept {channels_in} channel(s); required exactly {wanted}"


def iter_frame_blocks(
    data: np.ndarray, block_size: int, *, pad_tail: bool = False
) -> list[Chunk]:
    """Split (frames, channels) audio on frame boundaries.

    ``pad_tail`` zero-pads the final partial block so downstream block-based
    DSP always sees ``block_size`` frames; ``valid_frames`` records how much of
    the block is real so the caller can trim it back.
    """
    if data.ndim != 2:
        raise InputError("audio must be (frames, channels)", reason="bad_layout")
    if block_size < 1:
        raise InputError("block_size must be >= 1", reason="bad_block_size")
    frames = int(data.shape[0])
    if frames == 0:
        return []
    chunks: list[Chunk] = []
    for start in range(0, frames, block_size):
        stop = min(start + block_size, frames)
        block = np.ascontiguousarray(data[start:stop])
        valid = stop - start
        if pad_tail and valid < block_size:
            pad = np.zeros((block_size - valid, data.shape[1]), dtype=data.dtype)
            block = np.concatenate((block, pad), axis=0)
        chunks.append(Chunk(data=block, valid_frames=valid))
    return chunks


def apply_gain(data: np.ndarray, gain: float) -> np.ndarray:
    """Multiply by a scalar gain in float64 without mutating the input."""
    if gain == 1.0:
        return np.array(data, dtype=np.float64, copy=True)
    return (data.astype(np.float64, copy=False) * float(gain)).astype(np.float64, copy=False)


def truncate_tail(data: np.ndarray, frames: int) -> np.ndarray:
    """Trim (or pad) a block result back to the true frame count."""
    if frames < 0:
        raise InputError("negative frame count", reason="bad_frames")
    current = int(data.shape[0])
    if frames == current:
        return data
    if frames < current:
        return np.ascontiguousarray(data[:frames])
    pad = np.zeros((frames - current, data.shape[1]), dtype=data.dtype)
    return np.concatenate((data, pad), axis=0)
