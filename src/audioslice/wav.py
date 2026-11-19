"""PCM WAV loading and strict validation.

Only uncompressed integer PCM at 8/16/24/32 bits, mono or stereo, is accepted.
Every rejection raises :class:`WavFormatError` carrying a concrete reason that
the CLI mirrors to standard error.  Stereo material is down-mixed by a plain
channel mean so mono and stereo follow exactly the same analysis rules.
"""

from __future__ import annotations

import wave

import numpy as np

from .model import SUPPORTED_CHANNELS, SUPPORTED_SAMPLE_WIDTHS, AudioData


class WavFormatError(ValueError):
    """Raised when a WAV file is missing, corrupted or uses an unsupported format."""


def _decode_pcm(raw: bytes, sample_width: int) -> np.ndarray:
    """Decode little-endian integer PCM bytes to float64 in the range [-1, 1]."""
    if sample_width == 1:  # WAV 8-bit PCM is unsigned
        unsigned = np.frombuffer(raw, dtype=np.uint8)
        return (unsigned.astype(np.float64) - 128.0) / 128.0
    if sample_width == 2:
        return np.frombuffer(raw, dtype="<i2").astype(np.float64) / 32768.0
    if sample_width == 4:
        return np.frombuffer(raw, dtype="<i4").astype(np.float64) / 2147483648.0

    # 24-bit PCM has no native numpy dtype: assemble signed integers manually.
    octets = np.frombuffer(raw, dtype=np.uint8).reshape(-1, 3)
    signed24 = (
        octets[:, 0].astype(np.int32)
        | (octets[:, 1].astype(np.int32) << 8)
        | (octets[:, 2].astype(np.int32) << 16)
    )
    signed24 = np.where(signed24 & 0x00800000, signed24 - 0x01000000, signed24)
    return signed24.astype(np.float64) / 8388608.0


def load_wav(path: str) -> AudioData:
    """Read and validate ``path``, returning normalized mono :class:`AudioData`."""
    try:
        reader = wave.open(path, "rb")
    except FileNotFoundError:
        raise
    except wave.Error as exc:
        raise WavFormatError(f"not a valid WAV file: {exc}") from exc
    except OSError as exc:
        raise WavFormatError(f"cannot open WAV file: {exc}") from exc

    try:
        channels = reader.getnchannels()
        sample_width = reader.getsampwidth()
        sample_rate = reader.getframerate()
        frame_count = reader.getnframes()
        compression = reader.getcomptype()

        if compression != "NONE":
            raise WavFormatError(
                f"unsupported compression {reader.getcomptype()!r}; only uncompressed PCM is supported"
            )
        if channels not in SUPPORTED_CHANNELS:
            raise WavFormatError(f"unsupported channel count {channels}; only mono (1) and stereo (2) are supported")
        if sample_width not in SUPPORTED_SAMPLE_WIDTHS:
            raise WavFormatError(f"unsupported PCM bit depth {sample_width * 8}; supported: 8/16/24/32-bit")
        if sample_rate <= 0:
            raise WavFormatError(f"invalid sample rate {sample_rate}")
        if frame_count <= 0:
            raise WavFormatError("audio contains no samples")

        raw = reader.readframes(frame_count)
    except wave.Error as exc:
        raise WavFormatError(f"corrupt WAV data: {exc}") from exc
    finally:
        reader.close()

    expected_bytes = frame_count * channels * sample_width
    if len(raw) < expected_bytes:
        raise WavFormatError(
            f"truncated WAV data: expected {expected_bytes} bytes, found {len(raw)}"
        )

    samples = _decode_pcm(raw[:expected_bytes], sample_width)
    if not np.all(np.isfinite(samples)):
        raise WavFormatError("audio contains non-finite samples (NaN/Inf)")

    if channels == 2:
        interleaved = samples.reshape(frame_count, 2)
        mono = interleaved.mean(axis=1)
    else:
        mono = samples.astype(np.float64, copy=True)

    if mono.size == 0 or not np.all(np.isfinite(mono)):
        raise WavFormatError("audio contains no usable samples after decoding")

    return AudioData(
        samples=mono,
        sample_rate=sample_rate,
        channels=channels,
        sample_width_bytes=sample_width,
    )
