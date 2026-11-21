"""Audio file loading and channel-layout normalization."""

from __future__ import annotations

from pathlib import Path

import numpy as np
import soundfile as sf

from .errors import AssetError, ManifestError
from .project import ChannelLayout


def read_audio(path: str | Path) -> tuple[np.ndarray, int]:
    """Read a file as float64 with shape ``(frames, channels)``.

    Empty files yield a zero-row array carrying the file's channel count
    (mono when the container reports none).  Read failures raise
    :class:`AssetError` so the CLI can map them to exit code 3.
    """
    path = Path(path)
    try:
        info = sf.info(str(path))
    except (RuntimeError, OSError, ValueError) as exc:
        raise AssetError(f"cannot read audio file {path}: {exc}") from exc
    try:
        data, samplerate = sf.read(str(path), dtype="float64", always_2d=True)
    except (RuntimeError, OSError, ValueError) as exc:
        raise AssetError(f"cannot read audio file {path}: {exc}") from exc
    if data.size == 0:
        return np.zeros((0, max(info.channels, 1)), dtype=np.float64), samplerate
    return data, samplerate


def apply_layout(data: np.ndarray, layout: ChannelLayout,
                 project_channels: int, track_name: str) -> np.ndarray:
    """Convert source channels into the project channel layout.

    Incompatible layouts raise instead of silently dropping channels.
    Returned arrays are owned by the caller.
    """
    src_channels = data.shape[1]
    field = f"track {track_name}.layout"
    if layout.mode == "mono_copy":
        if src_channels != 1:
            raise ManifestError(
                f"mono_copy requires a mono source, got {src_channels} channels", field)
        return np.repeat(data, project_channels, axis=1)

    if layout.mode == "stereo":
        if src_channels != 2:
            raise ManifestError(
                f"stereo mode requires a stereo source, got {src_channels} channels", field)
        if project_channels == 1:
            return data.mean(axis=1, keepdims=True)
        out = np.zeros((data.shape[0], project_channels), dtype=np.float64)
        out[:, 0] = data[:, 0]
        out[:, 1] = data[:, 1]
        return out

    # Explicit downmix/upmix matrix: shape (project_channels, src_channels).
    matrix = layout.matrix
    if matrix is None:
        raise ManifestError("matrix mode requires a matrix", field)
    if len(matrix) != project_channels or any(len(row) != src_channels for row in matrix):
        raise ManifestError(
            f"matrix shape must be ({project_channels}, {src_channels})",
            f"{field}.matrix")
    coeff = np.asarray(matrix, dtype=np.float64)
    return data @ coeff.T
