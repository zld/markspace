"""Strict, streaming PCM WAV validation and reading."""
from __future__ import annotations

import os
import struct
from dataclasses import dataclass
from pathlib import Path
from typing import BinaryIO, Iterator

import numpy as np

_PCM_FORMAT = 1
_HEADER = struct.Struct("<4sI4s")
_CHUNK_HEADER = struct.Struct("<4sI")
_FMT = struct.Struct("<HHIIHH")
_WAVE = b"WAVE"
_RIFF = b"RIFF"


class WavError(ValueError):
    """Raised for any unreadable or unsupported WAV input."""

    def __init__(self, path: os.PathLike[str] | str, reason: str) -> None:
        self.path = str(path)
        self.reason = reason
        super().__init__(f"{self.path}: {reason}")


@dataclass(frozen=True)
class WavInfo:
    path: str
    sample_rate: int
    channels: int
    bits_per_sample: int
    total_frames: int
    data_offset: int
    data_size: int

    @property
    def total_samples(self) -> int:
        """Number of per-channel sample positions (audio frames)."""
        return self.total_frames

    @property
    def block_align(self) -> int:
        return self.channels * (self.bits_per_sample // 8)


def _read_exact(handle: BinaryIO, size: int, path: str, what: str) -> bytes:
    data = handle.read(size)
    if len(data) != size:
        raise WavError(
            path,
            f"truncated {what}: expected {size} bytes, found {len(data)}",
        )
    return data


def _parse_header(handle: BinaryIO, path: str, file_size: int) -> WavInfo:
    riff_id, riff_size, wave_id = _HEADER.unpack(
        _read_exact(handle, _HEADER.size, path, "RIFF header")
    )
    if riff_id != _RIFF or wave_id != _WAVE:
        raise WavError(path, "not a RIFF/WAVE file")
    if riff_size < 4:
        raise WavError(path, f"invalid RIFF size {riff_size}")

    fmt_data: bytes | None = None
    data_offset = 0
    data_size = 0
    offset = _HEADER.size
    while offset < file_size:
        if offset + _CHUNK_HEADER.size > file_size:
            raise WavError(path, f"truncated chunk header at byte {offset}")
        handle.seek(offset)
        chunk_id, chunk_size = _CHUNK_HEADER.unpack(
            _read_exact(handle, _CHUNK_HEADER.size, path, "chunk header")
        )
        payload_start = offset + _CHUNK_HEADER.size
        payload_end = payload_start + chunk_size
        if payload_end > file_size:
            name = chunk_id.decode("ascii", "replace")
            raise WavError(
                path,
                f"truncated {name!r} chunk at byte {payload_start}: "
                f"declares {chunk_size} bytes, {file_size - payload_start} available",
            )
        if chunk_id == b"fmt ":
            if fmt_data is not None:
                raise WavError(path, "duplicate fmt chunk")
            if chunk_size < 16:
                raise WavError(path, f"fmt chunk too small ({chunk_size} bytes)")
            handle.seek(payload_start)
            fmt_data = _read_exact(handle, 16, path, "fmt chunk")
        elif chunk_id == b"data":
            if fmt_data is None:
                raise WavError(path, "data chunk appears before fmt chunk")
            if data_offset:
                raise WavError(path, "multiple data chunks are not supported")
            data_offset = payload_start
            data_size = chunk_size
        offset = payload_end + (chunk_size & 1)

    if fmt_data is None:
        raise WavError(path, "missing fmt chunk")
    if not data_offset:
        raise WavError(path, "missing data chunk")

    audio_format, channels, sample_rate, byte_rate, block_align, bits = _FMT.unpack(
        fmt_data
    )
    if audio_format != _PCM_FORMAT:
        raise WavError(
            path,
            f"unsupported audio format {audio_format} (only 16-bit PCM is accepted)",
        )
    if channels < 1 or channels > 32:
        raise WavError(path, f"unsupported channel count {channels}")
    if sample_rate <= 0:
        raise WavError(path, f"invalid sample rate {sample_rate}")
    if bits != 16:
        raise WavError(path, f"unsupported bits per sample {bits} (expected 16)")
    expected_align = channels * 2
    if block_align != expected_align:
        raise WavError(
            path,
            f"invalid block alignment {block_align} (expected {expected_align})",
        )
    expected_rate = sample_rate * block_align
    if byte_rate != expected_rate:
        raise WavError(
            path,
            f"invalid byte rate {byte_rate} (expected {expected_rate})",
        )
    if data_size == 0:
        raise WavError(path, "empty data chunk (no audio samples)")
    if data_size % block_align:
        raise WavError(
            path,
            f"truncated data block: {data_size} bytes is not a multiple of "
            f"block alignment {block_align}",
        )
    return WavInfo(
        path=path,
        sample_rate=sample_rate,
        channels=channels,
        bits_per_sample=bits,
        total_frames=data_size // block_align,
        data_offset=data_offset,
        data_size=data_size,
    )


def inspect(path: os.PathLike[str] | str) -> WavInfo:
    """Validate *path* fully (headers only) and return stream metadata."""
    display = os.fspath(path)
    try:
        st = Path(path).stat()
    except FileNotFoundError:
        raise WavError(display, "file not found") from None
    except OSError as exc:
        raise WavError(display, f"cannot stat file: {exc.strerror or exc}") from exc
    if not os.path.isfile(path):
        raise WavError(display, "not a regular file")
    try:
        with open(path, "rb") as handle:
            return _parse_header(handle, display, st.st_size)
    except WavError:
        raise
    except PermissionError as exc:
        raise WavError(display, f"permission denied: {exc}") from exc
    except OSError as exc:
        raise WavError(display, f"cannot read file: {exc.strerror or exc}") from exc


class WavReader:
    """Validated WAV handle supporting constant-memory block iteration."""

    def __init__(self, path: os.PathLike[str] | str) -> None:
        self.info = inspect(path)
        self.path = self.info.path

    def iter_blocks(self, block_frames: int = 4096) -> Iterator[np.ndarray]:
        """Yield int16 arrays of shape ``(frames, channels)``."""
        if block_frames < 1:
            raise ValueError("block_frames must be positive")
        remaining = self.info.total_frames
        try:
            with open(self.path, "rb") as handle:
                handle.seek(self.info.data_offset)
                while remaining > 0:
                    frames = min(block_frames, remaining)
                    wanted = frames * self.info.block_align
                    raw = handle.read(wanted)
                    if len(raw) != wanted:
                        raise WavError(
                            self.path,
                            f"truncated data block at frame "
                            f"{self.info.total_frames - remaining}: "
                            f"expected {wanted} bytes, found {len(raw)}",
                        )
                    block = np.frombuffer(raw, dtype="<i2").reshape(
                        frames, self.info.channels
                    )
                    yield block
                    remaining -= frames
        except WavError:
            raise
        except PermissionError as exc:
            raise WavError(self.path, f"permission denied: {exc}") from exc
        except OSError as exc:
            raise WavError(
                self.path, f"cannot read file: {exc.strerror or exc}"
            ) from exc

    def __enter__(self) -> "WavReader":
        return self

    def __exit__(self, *exc: object) -> None:
        return None

