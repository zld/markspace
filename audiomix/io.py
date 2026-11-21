"""WAV file reading/writing and channel mapping."""

from __future__ import annotations

import numpy as np
import soundfile as sf

from .model import MAX_TRACK_SECONDS


class AudioIOError(Exception):
    """Raised for unreadable/invalid audio files and write failures."""


def read_wav(path: str, track_index: int) -> tuple[np.ndarray, int]:
    """Read a WAV file as float64 (frames, channels).

    Rejects files with more than 2 channels, non-finite samples, or a
    duration over the per-track limit.
    """
    try:
        data, rate = sf.read(path, dtype="float64", always_2d=True)
    except Exception as exc:
        raise AudioIOError(f"track {track_index}: cannot read {path}: {exc}")
    if data.shape[0] == 0:
        raise AudioIOError(f"track {track_index}: {path} contains no frames")
    channels = data.shape[1]
    if channels not in (1, 2):
        raise AudioIOError(
            f"track {track_index}: {channels} channels not supported (mono/stereo only)"
        )
    duration = data.shape[0] / float(rate)
    if duration > MAX_TRACK_SECONDS:
        raise AudioIOError(
            f"track {track_index}: duration {duration:.1f}s exceeds "
            f"{MAX_TRACK_SECONDS:.0f}s limit"
        )
    if not np.isfinite(data).all():
        raise AudioIOError(f"track {track_index}: {path} contains NaN or Inf samples")
    return data, int(rate)


def map_channels(data: np.ndarray, target_channels: int) -> np.ndarray:
    """Convert (frames, channels) to the target channel count.

    Mono is duplicated to stereo; stereo is averaged to mono.  Any other
    combination is rejected.
    """
    src = data.shape[1]
    if src == target_channels:
        return data
    if src == 1 and target_channels == 2:
        return np.repeat(data, 2, axis=1)
    if src == 2 and target_channels == 1:
        return data.mean(axis=1, keepdims=True)
    raise AudioIOError(
        f"cannot map {src} channels to {target_channels} channels"
    )


def write_wav(path: str, data: np.ndarray, rate: int) -> None:
    """Write float32 WAV, raising AudioIOError on failure."""
    try:
        sf.write(path, np.asarray(data, dtype=np.float32), rate, subtype="FLOAT")
    except Exception as exc:
        raise AudioIOError(f"cannot write {path}: {exc}")
