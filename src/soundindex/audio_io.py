"""PCM WAV reading: validation of rate/channels/depth and chunked decoding.

Files are decoded in bounded chunks so 30-minute recordings stay well
under the 512 MiB memory budget.
"""
from __future__ import annotations

import os
import wave

import numpy as np

from .models import FileInfo

MIN_SAMPLE_RATE = 8000
MAX_SAMPLE_RATE = 192000
MAX_DURATION_S = 30 * 60
CHUNK_FRAMES = 1 << 20  # ~1M frames per read keeps peak memory bounded


class AudioError(Exception):
    """File-level audio decoding/validation failure (exit code 1 path)."""


def _probe(path: str) -> wave.Wave_read:
    if not os.path.exists(path):
        raise AudioError(f"file not found: {path}")
    if os.path.getsize(path) == 0:
        raise AudioError("empty file")
    try:
        return wave.open(path, "rb")
    except wave.Error as exc:
        raise AudioError(f"not a PCM WAV file: {exc}") from exc
    except EOFError as exc:
        raise AudioError("truncated WAV header") from exc


def _info_from(path: str, w: wave.Wave_read) -> FileInfo:
    if w.getcomptype() != "NONE":
        raise AudioError(f"unsupported compression: {w.getcomptype()}")
    channels = w.getnchannels()
    if channels < 1:
        raise AudioError("file has no audio channels")
    rate = w.getframerate()
    if not (MIN_SAMPLE_RATE <= rate <= MAX_SAMPLE_RATE):
        raise AudioError(f"sample rate {rate} Hz out of supported range")
    width = w.getsampwidth()
    if width not in (1, 2, 3, 4):
        raise AudioError(f"unsupported bit depth: {width * 8}")
    n = w.getnframes()
    if rate > 0 and n / rate > MAX_DURATION_S:
        raise AudioError("audio exceeds the 30 minute limit")
    return FileInfo(path=path, sample_rate=rate, channels=channels,
                    bit_depth=width * 8, n_samples=n)


def read_info(path: str) -> FileInfo:
    with _probe(path) as w:
        return _info_from(path, w)


def _decode(raw: bytes, width: int, channels: int) -> np.ndarray:
    """Decode little-endian PCM to float32 in [-1, 1], shape (n, channels)."""
    if width == 1:
        data = (np.frombuffer(raw, dtype=np.uint8).astype(np.float32) - 128.0) / 128.0
    elif width == 2:
        data = np.frombuffer(raw, dtype="<i2").astype(np.float32) / 32768.0
    elif width == 3:
        b = np.frombuffer(raw, dtype=np.uint8).reshape(-1, 3).astype(np.int32)
        v = b[:, 0] | (b[:, 1] << 8) | (b[:, 2] << 16)
        v = np.where(v & 0x800000, v - 0x1000000, v)
        data = v.astype(np.float32) / 8388608.0
    else:
        data = np.frombuffer(raw, dtype="<i4").astype(np.float32) / 2147483648.0
    return data.reshape(-1, channels)


def open_wav(path: str, chunk_frames: int = CHUNK_FRAMES):
    """Return (FileInfo, chunk iterator of float32 arrays shaped (n, ch)).

    Raises AudioError for empty files, non-PCM encodings, out-of-range
    sample rates and truncated sample data.
    """
    w = _probe(path)
    info = _info_from(path, w)
    frame_bytes = (info.bit_depth // 8) * info.channels

    def gen():
        with w:
            remaining = info.n_samples
            while remaining > 0:
                want = min(chunk_frames, remaining)
                raw = w.readframes(want)
                got = len(raw) // frame_bytes
                if got:
                    yield _decode(raw[: got * frame_bytes], info.bit_depth // 8,
                                  info.channels)
                remaining -= got
                if got < want:
                    raise AudioError("truncated WAV sample data")

    return info, gen()
