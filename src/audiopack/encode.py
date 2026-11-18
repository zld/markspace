"""WAV encoding with atomic writes and read-back verification."""

from __future__ import annotations

import os
import tempfile
from pathlib import Path

import numpy as np
import soundfile as sf

from audiopack.loudness import count_clipped
from audiopack.models import OUTPUT_FORMAT, OUTPUT_SUBTYPE, AudioMeta, EncodeError

# A re-read PCM_24 sample at full scale counts as clipped.
PCM24_CLIP_LIMIT = 1.0 - 2.0**-24


def write_wav_atomic(path: Path, data: np.ndarray, sample_rate: int) -> None:
    """Write ``data`` as PCM_24 WAV, atomically replacing ``path``.

    The payload is first written to a temporary file in the same directory and
    then renamed, so an interrupt or write failure never leaves a
    pseudo-complete WAV behind.
    """
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(prefix=path.name + ".", suffix=".tmp", dir=path.parent)
    os.close(fd)
    try:
        sf.write(tmp_name, data, sample_rate, subtype=OUTPUT_SUBTYPE, format=OUTPUT_FORMAT)
        os.replace(tmp_name, path)
    except Exception as exc:
        try:
            os.unlink(tmp_name)
        except OSError:
            pass
        if isinstance(exc, EncodeError):
            raise
        raise EncodeError(f"failed to write {path}: {exc}") from exc


def verify_wav(
    path: Path, expected_rate: int, expected_channels: int, expected_frames: int
) -> tuple[AudioMeta, int]:
    """Re-read a written WAV and validate rate, frames, channels, clipping."""
    try:
        info = sf.info(path)
    except Exception as exc:
        raise EncodeError(f"cannot re-read written file {path}: {exc}") from exc
    problems = []
    if info.samplerate != expected_rate:
        problems.append(f"sample rate {info.samplerate} != {expected_rate}")
    if info.channels != expected_channels:
        problems.append(f"channels {info.channels} != {expected_channels}")
    if info.frames != expected_frames:
        problems.append(f"frames {info.frames} != {expected_frames}")
    if problems:
        raise EncodeError(f"verification failed for {path}: " + "; ".join(problems))
    data = sf.read(path, dtype="float32", always_2d=True)[0]
    clipped = count_clipped(data, limit=PCM24_CLIP_LIMIT)
    meta = AudioMeta(
        path=path,
        sample_rate=info.samplerate,
        channels=info.channels,
        frames=info.frames,
        format=info.format,
        subtype=info.subtype,
    )
    return meta, clipped
