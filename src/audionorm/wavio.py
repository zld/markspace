"""WAV reading, guarded atomic writing, and metadata verification.

Reads any local WAV (PCM or float) as float64; writes PCM WAV only.
Writes go to a temporary sibling file, are re-opened for a readability
and metadata check, and only then atomically replace the target, so a
failed write never leaves a half-written file that looks successful.
"""
from __future__ import annotations

import os
import uuid
from pathlib import Path

import numpy as np
import soundfile as sf

from .errors import E_DECODE, E_INVALID_DATA, E_LIMIT, E_WRITE, AudioNormError

MAX_FILE_BYTES = 200 * 1024 * 1024
MAX_CHANNELS = 8
MIN_RATE = 8000
MAX_RATE = 384000
MAX_FRAMES = 100_000_000
OUTPUT_SUBTYPE = "PCM_16"


def read_wav(path: Path) -> tuple[np.ndarray, int]:
    """Read a WAV file as (frames, channels) float64 plus its sample rate."""
    try:
        size = path.stat().st_size
    except OSError as exc:
        raise AudioNormError(E_DECODE, f"cannot stat: {exc}") from exc
    if size > MAX_FILE_BYTES:
        raise AudioNormError(E_LIMIT, f"file too large: {size} bytes")
    try:
        info = sf.info(str(path))
    except Exception as exc:
        raise AudioNormError(E_DECODE, f"unreadable WAV: {exc}") from exc
    if not info.samplerate or info.samplerate <= 0:
        raise AudioNormError(E_DECODE, "missing or invalid sample rate")
    if not (MIN_RATE <= info.samplerate <= MAX_RATE):
        raise AudioNormError(E_LIMIT, f"sample rate out of range: {info.samplerate}")
    if info.channels < 1 or info.channels > MAX_CHANNELS:
        raise AudioNormError(E_LIMIT, f"channel count out of range: {info.channels}")
    if info.frames > MAX_FRAMES:
        raise AudioNormError(E_LIMIT, f"too many frames: {info.frames}")
    try:
        data = sf.read(str(path), dtype="float64", always_2d=True)[0]
    except Exception as exc:
        raise AudioNormError(E_DECODE, f"decode failed: {exc}") from exc
    if not np.all(np.isfinite(data)):
        raise AudioNormError(E_INVALID_DATA, "NaN or Inf samples present")
    return data, int(info.samplerate)


def write_wav_atomic(path: Path, data: np.ndarray, rate: int) -> None:
    """Write PCM WAV via temp file + verification + atomic replace."""
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(f".{path.name}.{uuid.uuid4().hex}.tmp")
    try:
        clipped = np.clip(data, -1.0, 1.0)
        sf.write(str(tmp), clipped, rate, subtype=OUTPUT_SUBTYPE, format="WAV")
        info = sf.info(str(tmp))  # readability check
        if (info.samplerate != rate or info.channels != data.shape[1]
                or info.frames != data.shape[0]):
            raise AudioNormError(
                E_WRITE,
                f"verification mismatch: rate={info.samplerate} "
                f"channels={info.channels} frames={info.frames}")
        os.replace(tmp, path)
    except AudioNormError:
        _cleanup(tmp)
        raise
    except Exception as exc:
        _cleanup(tmp)
        raise AudioNormError(E_WRITE, f"write failed: {exc}") from exc


def _cleanup(tmp: Path) -> None:
    try:
        tmp.unlink(missing_ok=True)
    except OSError:
        pass
