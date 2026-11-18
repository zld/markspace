"""PCM WAV reader: validates headers and normalises samples to float32."""
from __future__ import annotations

import wave

import numpy as np

from .models import AudioData


class WavError(Exception):
    """Raised when a file is not a valid, fully intact PCM WAV."""


_SUPPORTED_WIDTHS = (1, 2, 3, 4)
_MAX_CHANNELS = 8


def _decode_pcm(raw: bytes, width: int, channels: int) -> np.ndarray:
    """Convert raw little-endian PCM bytes to float32 in [-1, 1)."""
    if width == 1:  # WAV 8-bit PCM is unsigned
        data = np.frombuffer(raw, dtype=np.uint8).astype(np.float32)
        data = (data - 128.0) / 128.0
    elif width == 2:
        data = np.frombuffer(raw, dtype="<i2").astype(np.float32) / 32768.0
    elif width == 4:
        data = np.frombuffer(raw, dtype="<i4").astype(np.float32) / 2147483648.0
    else:  # 24-bit: pack into int32 manually
        bytes_in = np.frombuffer(raw, dtype=np.uint8).reshape(-1, 3)
        shifted = np.zeros(bytes_in.shape[0], dtype="<i4")
        view = shifted.view(np.uint8).reshape(-1, 4)
        view[:, :3] = bytes_in
        view[:, 3] = bytes_in[:, 2]  # sign-extend
        data = shifted.astype(np.float32) / 8388608.0
    return data.reshape(-1, channels)


def read_wav(path: str) -> AudioData:
    """Read a PCM WAV file and return normalised multi-channel samples.

    Raises WavError for empty files, non-PCM formats, unsupported sample
    widths/channel counts and truncated frame data.
    """
    try:
        with wave.open(path, "rb") as wf:
            channels = wf.getnchannels()
            sample_width = wf.getsampwidth()
            sample_rate = wf.getframerate()
            frame_count = wf.getnframes()
            compression = wf.getcomptype()
            raw = wf.readframes(frame_count)
    except wave.Error as exc:  # non-PCM or malformed RIFF structure
        raise WavError(f"not a PCM WAV file: {exc}") from exc
    except OSError as exc:
        raise WavError(f"cannot read file: {exc}") from exc

    if compression != "NONE":
        raise WavError(f"unsupported compression {compression!r}; PCM only")
    if sample_rate <= 0:
        raise WavError(f"invalid sample rate: {sample_rate}")
    if not 1 <= channels <= _MAX_CHANNELS:
        raise WavError(f"invalid channel count: {channels}")
    if sample_width not in _SUPPORTED_WIDTHS:
        raise WavError(f"unsupported sample width: {sample_width} bytes")
    if frame_count <= 0:
        raise WavError("empty WAV: no audio frames")

    expected = frame_count * channels * sample_width
    if len(raw) < expected:
        raise WavError(
            f"truncated frames: header declares {frame_count} frames "
            f"({expected} bytes) but only {len(raw)} data bytes present"
        )

    samples = _decode_pcm(raw[:expected], sample_width, channels)
    return AudioData(
        path=path,
        samples=np.ascontiguousarray(samples),
        sample_rate=sample_rate,
        channels=channels,
        sample_width=sample_width,
    )
