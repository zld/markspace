"""WAV reading/writing via soundfile with format validation.

Only WAV/WAVEX containers with PCM integer or IEEE float subtypes are
accepted. Reads reject corrupt headers, non-finite samples, and inputs beyond
the configured duration/channel limits. Writes go to a temporary file in the
destination directory and are atomically renamed, so an interrupted run never
leaves a truncated file masquerading as a complete WAV at the target path.
"""
from __future__ import annotations

import os
import tempfile
from pathlib import Path

import numpy as np
import soundfile as sf

ALLOWED_FORMATS = frozenset({"WAV", "WAVEX"})
ALLOWED_SUBTYPES = frozenset(
    {"PCM_U8", "PCM_16", "PCM_24", "PCM_32", "FLOAT", "DOUBLE"})
OUTPUT_SUBTYPE = "FLOAT"


class WavValidationError(ValueError):
    """Input WAV violates format or configured resource limits."""


def read_wav(path: str | Path, max_channels: int,
             max_seconds: float) -> tuple[np.ndarray, int]:
    """Read and validate a WAV file; returns (frames, channels) float64 + rate."""
    path = Path(path)
    info = sf.info(path)  # raises on corrupt/unsupported headers
    if info.format not in ALLOWED_FORMATS:
        raise WavValidationError(f"{path}: container {info.format!r} is not WAV")
    if info.subtype not in ALLOWED_SUBTYPES:
        raise WavValidationError(f"{path}: subtype {info.subtype!r} not PCM/float")
    if info.samplerate <= 0:
        raise WavValidationError(f"{path}: invalid sample rate {info.samplerate}")
    if not 1 <= info.channels <= max_channels:
        raise WavValidationError(
            f"{path}: {info.channels} channels exceed limit {max_channels}")
    if info.frames <= 0:
        raise WavValidationError(f"{path}: no audio frames")
    if info.frames / info.samplerate > max_seconds:
        raise WavValidationError(
            f"{path}: duration {info.frames / info.samplerate:.1f}s "
            f"exceeds limit {max_seconds}s")
    data = sf.read(path, dtype="float64", always_2d=True)[0]
    if data.shape[0] != info.frames:
        raise WavValidationError(
            f"{path}: truncated payload ({data.shape[0]}/{info.frames} frames)")
    if not np.isfinite(data).all():
        raise WavValidationError(f"{path}: non-finite sample values")
    return data, int(info.samplerate)


def write_wav_atomic(path: str | Path, frames: np.ndarray, rate: int,
                     subtype: str = OUTPUT_SUBTYPE) -> None:
    """Write to a temp file in the same directory, then atomically replace."""
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=path.parent,
                               prefix=f".{path.name}.tmp-", suffix=".wav")
    os.close(fd)
    try:
        sf.write(tmp, np.asarray(frames, dtype=np.float64), int(rate),
                 subtype=subtype)
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise
