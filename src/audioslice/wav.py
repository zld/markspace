"""Streaming PCM WAV reader/writer with strict format validation.

Only integer PCM (including the WAVE_FORMAT_EXTENSIBLE PCM subtype) is
accepted.  Floating point WAVs, truncated frames, unknown sample formats and
sample rates outside :data:`SUPPORTED_SAMPLE_RATES` are rejected with a
:class:`~audioslice.errors.WavFormatError` that names the offending path.

Reads are framed and streamed: callers pull fixed-size blocks, so memory use
stays bounded regardless of file length.
"""

from __future__ import annotations

import os
import struct
from dataclasses import dataclass

import numpy as np

from .errors import InputFileError, WavFormatError

SUPPORTED_SAMPLE_RATES = frozenset({8000, 16000, 22050, 24000, 32000, 44100, 48000, 96000})

_WAVE_FORMAT_PCM = 0x0001
_WAVE_FORMAT_EXTENSIBLE = 0xFFFE
_PCM_SUBTYPE = b"\x01\x00\x00\x00\x00\x00\x10\x00\x80\x00\x00\xaa\x00\x38\x9b\x71"
_RIFF_HEADER = b"RIFF"
_WAVE_ID = b"WAVE"
_FMT_ID = b"fmt "
_DATA_ID = b"data"
_INT_DTYPES = {1: np.uint8, 2: "<i2", 3: None, 4: "<i4"}


@dataclass(frozen=True)
class WavInfo:
    """Validated header information for an opened WAV file."""

    path: str
    sample_rate: int
    channels: int
    sample_width: int
    frame_count: int

    @property
    def duration_seconds(self) -> float:
        return self.frame_count / self.sample_rate


class WavReader:
    """Context-managed streaming reader producing interleaved ``(n, ch)`` blocks."""

    def __init__(self, path: str) -> None:
        self.path = path
        try:
            self._fh = open(path, "rb")
        except FileNotFoundError as exc:
            raise InputFileError(path, "file does not exist") from exc
        except OSError as exc:
            raise InputFileError(path, f"cannot open file: {exc.strerror or exc}") from exc
        try:
            self._data_start = 0
            self.info = self._parse_header()
        except Exception:
            self._fh.close()
            raise
        self._frames_read = 0

    # ------------------------------------------------------------------ header
    def _read_exact(self, n: int, what: str) -> bytes:
        data = self._fh.read(n)
        if len(data) != n:
            raise WavFormatError(self.path, f"truncated file while reading {what}")
        return data

    def _parse_header(self) -> WavInfo:
        if self._fh.read(4) != _RIFF_HEADER:
            raise WavFormatError(self.path, "not a RIFF container")
        self._read_exact(4, "RIFF size")
        if self._fh.read(4) != _WAVE_ID:
            raise WavFormatError(self.path, "RIFF container is not WAVE audio")

        fmt = None
        data_size = None
        while True:
            chunk_header = self._fh.read(8)
            if not chunk_header:
                break
            if len(chunk_header) != 8:
                raise WavFormatError(self.path, "truncated chunk header")
            chunk_id, chunk_size = struct.unpack("<4sI", chunk_header)
            padded_size = chunk_size + (chunk_size & 1)
            if chunk_id == _FMT_ID:
                fmt = self._read_exact(chunk_size, "fmt chunk")
                self._fh.seek(padded_size - chunk_size, os.SEEK_CUR)
            elif chunk_id == _DATA_ID:
                self._data_start = self._fh.tell()
                data_size = chunk_size
                break
            else:
                self._fh.seek(padded_size, os.SEEK_CUR)
        if fmt is None:
            raise WavFormatError(self.path, "missing fmt chunk")
        if data_size is None:
            raise WavFormatError(self.path, "missing data chunk")

        tag, channels, sample_rate, _, _, bits = struct.unpack("<HHIIHH", fmt[:16])
        if tag == _WAVE_FORMAT_EXTENSIBLE:
            if len(fmt) < 40 or fmt[24:40] != _PCM_SUBTYPE:
                raise WavFormatError(self.path, "only integer PCM extensible streams are supported")
        elif tag != _WAVE_FORMAT_PCM:
            raise WavFormatError(
                self.path,
                f"floating point or compressed audio (format tag 0x{tag:04x}) is not supported",
            )
        if sample_rate not in SUPPORTED_SAMPLE_RATES:
            raise WavFormatError(self.path, f"unsupported sample rate {sample_rate} Hz")
        if channels < 1:
            raise WavFormatError(self.path, "channel count must be at least 1")
        width = bits // 8
        if bits % 8 or width not in _INT_DTYPES:
            raise WavFormatError(self.path, f"unsupported bit depth {bits}")
        frame_size = width * channels
        if frame_size == 0 or data_size % frame_size:
            raise WavFormatError(self.path, "data chunk does not hold whole audio frames")
        self._data_size = data_size
        self._frame_size = frame_size
        return WavInfo(self.path, sample_rate, channels, width, data_size // frame_size)

    # ------------------------------------------------------------------- reads
    @property
    def frames_remaining(self) -> int:
        return self.info.frame_count - self._frames_read

    def read_frames(self, max_frames: int) -> np.ndarray:
        """Return up to ``max_frames`` frames as an interleaved ``(n, ch)`` array."""
        if max_frames <= 0:
            raise ValueError("max_frames must be positive")
        wanted = min(max_frames, self.frames_remaining)
        if wanted == 0:
            return np.empty((0, self.info.channels), dtype=np.float64)
        raw = self._fh.read(wanted * self._frame_size)
        if len(raw) != wanted * self._frame_size:
            raise WavFormatError(self.path, "truncated PCM data: fewer bytes than header declares")
        samples = self._decode(raw)
        frames = samples.reshape(-1, self.info.channels)
        self._frames_read += frames.shape[0]
        return frames

    def _decode(self, raw: bytes) -> np.ndarray:
        width = self.info.sample_width
        if width == 1:  # 8-bit PCM is unsigned, centred at 128
            return np.frombuffer(raw, dtype=np.uint8).astype(np.float64) - 128.0
        if width == 3:  # 24-bit little-endian signed, no native numpy dtype
            triple = np.frombuffer(raw, dtype=np.uint8).reshape(-1, 3).astype(np.int32)
            value = triple[:, 0] | (triple[:, 1] << 8) | (triple[:, 2] << 16)
            value = np.where(value & 0x800000, value - 0x1000000, value)
            return value.astype(np.float64)
        return np.frombuffer(raw, dtype=_INT_DTYPES[width]).astype(np.float64)

    def rewind(self) -> None:
        """Return to the first PCM frame so the file can be scanned twice."""
        self._fh.seek(self._data_start)
        self._frames_read = 0

    def close(self) -> None:
        self._fh.close()

    def __enter__(self) -> "WavReader":
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()


def write_pcm_wav(path: str, frames: np.ndarray, sample_rate: int, sample_width: int = 2) -> None:
    """Write an interleaved integer PCM WAV (used by fixtures and tests).

    ``frames`` is a 1-D mono array or ``(n, channels)`` array.  Audio samples
    are integer PCM amplitudes; the function never overwrites an input it did
    not create — callers choose the destination path.
    """
    if sample_width not in (1, 2, 3, 4):
        raise ValueError("sample_width must be one of 1, 2, 3, 4")
    arr = np.asarray(frames)
    if arr.ndim == 1:
        arr = arr[:, None]
    channels = arr.shape[1]
    clipped = np.clip(np.rint(arr), -(1 << (8 * sample_width - 1)), (1 << (8 * sample_width - 1)) - 1)

    if sample_width == 1:
        payload = (clipped + 128).astype(np.uint8).tobytes()
    elif sample_width == 2:
        payload = clipped.astype("<i2").tobytes()
    elif sample_width == 4:
        payload = clipped.astype("<i4").tobytes()
    else:
        flat = clipped.astype(np.int32).reshape(-1)
        out = np.empty((flat.size, 3), dtype=np.uint8)
        out[:, 0] = flat & 0xFF
        out[:, 1] = (flat >> 8) & 0xFF
        out[:, 2] = (flat >> 16) & 0xFF
        payload = out.tobytes()

    byte_rate = sample_rate * channels * sample_width
    block_align = channels * sample_width
    header = b"RIFF" + struct.pack("<I", 36 + len(payload)) + b"WAVE"
    header += b"fmt " + struct.pack("<IHHIIHH", 16, 1, channels, sample_rate,
                                    byte_rate, block_align, sample_width * 8)
    header += b"data" + struct.pack("<I", len(payload))
    with open(path, "wb") as fh:
        fh.write(header + payload)
