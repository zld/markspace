"""Chunked reading and decoding of integer PCM WAV files.

Only uncompressed integer PCM is accepted: 8/16/24/32-bit samples,
1-8 channels, 8-192 kHz. Files are decoded in blocks of at most one
second so memory stays bounded regardless of file length. Anything
else (compressed codecs, empty files, truncated frames, invalid
headers) raises WavError so the caller can mark that single file as
failed and continue with the rest of the batch.
"""
from __future__ import annotations

import wave
from dataclasses import dataclass
from typing import Iterator

import numpy as np

from . import (
    MAX_CHANNELS,
    MAX_SAMPLE_RATE,
    MIN_CHANNELS,
    MIN_SAMPLE_RATE,
    PCM_SAMPLE_WIDTHS,
)

# Never decode more than this many frames at once (1 second of audio
# at the highest accepted sample rate is the upper bound).
MAX_CHUNK_SECONDS = 1.0


class WavError(Exception):
    """A single input file is unreadable or not integer PCM WAV."""


@dataclass(frozen=True)
class WavInfo:
    sample_rate: int
    channels: int
    sample_frames: int  # as declared by the header
    sample_width: int  # bytes per sample


def _open(path: str) -> wave.Wave_read:
    try:
        return wave.open(str(path), "rb")
    except (wave.Error, EOFError, OSError) as exc:
        raise WavError(f"{path}: not a readable PCM WAV file ({exc})") from exc


def read_info(path: str) -> WavInfo:
    """Open a WAV file and validate that it is supported integer PCM."""
    with _open(path) as wf:
        return _validate(path, wf)


def _validate(path: str, wf: wave.Wave_read) -> WavInfo:
    if wf.getcomptype() != "NONE":
        raise WavError(
            f"{path}: compressed WAV ({wf.getcomptype()}) is not supported"
        )
    channels = wf.getnchannels()
    if not (MIN_CHANNELS <= channels <= MAX_CHANNELS):
        raise WavError(f"{path}: unsupported channel count {channels}")
    rate = wf.getframerate()
    if not (MIN_SAMPLE_RATE <= rate <= MAX_SAMPLE_RATE):
        raise WavError(f"{path}: unsupported sample rate {rate}")
    width = wf.getsampwidth()
    if width not in PCM_SAMPLE_WIDTHS:
        raise WavError(f"{path}: unsupported sample width {width * 8} bits")
    return WavInfo(
        sample_rate=rate,
        channels=channels,
        sample_frames=wf.getnframes(),
        sample_width=width,
    )


def decode_block(data: bytes, sample_width: int, channels: int) -> np.ndarray:
    """Decode raw PCM bytes to a float64 array of shape (frames, channels).

    Values are normalised to [-1.0, 1.0]. 8-bit PCM is unsigned with a
    128 offset; 24-bit is sign-extended from three little-endian bytes.
    """
    if sample_width == 1:
        samples = (np.frombuffer(data, dtype=np.uint8).astype(np.int16) - 128)
        out = samples.astype(np.float64) / 128.0
    elif sample_width == 2:
        out = np.frombuffer(data, dtype="<i2").astype(np.float64) / 32768.0
    elif sample_width == 3:
        raw = np.frombuffer(data, dtype=np.uint8).reshape(-1, 3)
        padded = np.zeros((raw.shape[0], 4), dtype=np.uint8)
        padded[:, 1:] = raw  # little-endian: value sits in the top 3 bytes
        out = (padded.view("<i4").reshape(-1) >> 8).astype(np.float64) / 8388608.0
    elif sample_width == 4:
        out = np.frombuffer(data, dtype="<i4").astype(np.float64) / 2147483648.0
    else:  # pragma: no cover - guarded by _validate
        raise WavError(f"unsupported sample width {sample_width}")
    return out.reshape(-1, channels)


def iter_chunks(path: str) -> Iterator[tuple]:
    """Yield (WavInfo, block) pairs; the first yield carries the info.

    Each block is a float64 array of shape (frames, channels) covering
    at most MAX_CHUNK_SECONDS of audio. Raises WavError if the file is
    truncated (fewer frames than the header declares, or a partial
    trailing frame).
    """
    with _open(path) as wf:
        info = _validate(path, wf)
        frame_bytes = info.sample_width * info.channels
        chunk_frames = max(1, int(info.sample_rate * MAX_CHUNK_SECONDS))
        frames_read = 0
        while True:
            data = wf.readframes(chunk_frames)
            if not data:
                break
            if len(data) % frame_bytes != 0:
                raise WavError(f"{path}: truncated frame at end of data chunk")
            frames_read += len(data) // frame_bytes
            yield info, decode_block(data, info.sample_width, info.channels)
        if frames_read < info.sample_frames:
            raise WavError(
                f"{path}: truncated file, header declares "
                f"{info.sample_frames} frames but only {frames_read} present"
            )
