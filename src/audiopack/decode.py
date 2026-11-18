"""Decode audio files to interleaved float frames with validation."""

from __future__ import annotations

from pathlib import Path

import numpy as np
import soundfile as sf

from audiopack.models import (
    MAX_CHANNELS,
    MIN_CHANNELS,
    AudioMeta,
    DecodeError,
)


def decode_file(path: Path, meta: AudioMeta | None = None) -> tuple[np.ndarray, AudioMeta]:
    """Decode ``path`` to a float32 array of shape (frames, channels).

    Raises DecodeError for unreadable files, zero-frame content, unsupported
    channel layouts, and non-finite samples.
    """
    try:
        data, _ = sf.read(path, dtype="float32", always_2d=True)
    except Exception as exc:
        raise DecodeError(f"failed to decode {path.name}: {exc}") from exc

    info = sf.info(path)
    frames, channels = data.shape
    if frames == 0:
        raise DecodeError(f"zero-frame file: {path.name}")
    if not (MIN_CHANNELS <= channels <= MAX_CHANNELS):
        raise DecodeError(
            f"unsupported channel count {channels} in {path.name} "
            f"(allowed {MIN_CHANNELS}..{MAX_CHANNELS})"
        )
    if not np.isfinite(data).all():
        raise DecodeError(f"non-finite samples in {path.name}")

    if meta is None:
        meta = AudioMeta(
            path=path,
            sample_rate=info.samplerate,
            channels=channels,
            frames=frames,
            format=info.format,
            subtype=info.subtype,
        )
    return data, meta
