"""WAV reading and normalization to mono float64 samples."""

from __future__ import annotations

import hashlib
import os
import struct
from dataclasses import dataclass
from pathlib import Path

import numpy as np

from .config import ConfigError, DetectionConfig

_PCM = 1
_IEEE_FLOAT = 3
_EXTENSIBLE = 0xFFFE
_CHUNK = 1024 * 1024


class AudioError(Exception):
    """Raised when an input cannot be read as valid PCM audio."""


@dataclass(frozen=True)
class AudioData:
    samples: np.ndarray
    sample_rate: int
    channels: int
    source_bits: int
    frame_count: int


def _read_exact(handle, size: int) -> bytes:
    data = handle.read(size)
    if len(data) != size:
        raise AudioError("truncated WAV file")
    return data


def _parse_fmt(payload: bytes) -> tuple[int, int, int, int]:
    if len(payload) < 16:
        raise AudioError("fmt chunk too short")
    tag, channels, rate, _byte_rate, block_align, bits = struct.unpack(
        "<HHIIHH", payload[:16]
    )
    if tag == _EXTENSIBLE:
        if len(payload) < 40:
            raise AudioError("extensible fmt chunk too short")
        tag = struct.unpack("<H", payload[24:26])[0]
    if tag not in (_PCM, _IEEE_FLOAT):
        raise AudioError(f"unsupported WAV encoding tag {tag}")
    if channels < 1 or channels > 32:
        raise AudioError(f"unsupported channel count {channels}")
    if bits not in (8, 16, 24, 32, 64):
        raise AudioError(f"unsupported sample width {bits}")
    if tag == _IEEE_FLOAT and bits not in (32, 64):
        raise AudioError(f"invalid float sample width {bits}")
    width = bits // 8
    if block_align != channels * width:
        raise AudioError("inconsistent block alignment")
    return tag, channels, rate, bits


def _decode(payload: bytes, tag: int, channels: int, bits: int) -> np.ndarray:
    width = bits // 8
    usable = len(payload) - (len(payload) % (channels * width))
    raw = payload[:usable]
    if tag == _IEEE_FLOAT:
        dtype = "<f4" if bits == 32 else "<f8"
        data = np.frombuffer(raw, dtype=dtype).astype(np.float64)
    elif bits == 8:
        data = np.frombuffer(raw, dtype=np.uint8).astype(np.float64)
        data -= 128.0
        data /= 128.0
    elif bits == 16:
        data = np.frombuffer(raw, dtype="<i2").astype(np.float64)
        data /= 32768.0
    elif bits == 24:
        triples = np.frombuffer(raw, dtype=np.uint8).reshape(-1, 3).astype(np.int32)
        values = triples[:, 0] | (triples[:, 1] << 8) | (triples[:, 2] << 16)
        values = np.where(values >= 1 << 23, values - (1 << 24), values)
        data = values.astype(np.float64) / float(1 << 23)
    else:
        data = np.frombuffer(raw, dtype="<i4").astype(np.float64)
        data /= float(1 << 31)
    if not data.size:
        return np.zeros(0, dtype=np.float64)
    if not np.all(np.isfinite(data)):
        raise AudioError("WAV contains non-finite samples")
    if data.size % channels:
        raise AudioError("partial sample frame at end of WAV")
    data = data.reshape(-1, channels)
    if channels == 1:
        return data.reshape(-1)
    return data.mean(axis=1)


def fingerprint_file(path: Path, config: DetectionConfig) -> str:
    """Return a SHA-256 digest of the raw input bytes."""
    try:
        stat = os.stat(path)
    except OSError as exc:
        raise AudioError(f"cannot stat input: {exc.strerror}") from exc
    if stat.st_size > int(config.max_file_mb * 1024 * 1024):
        raise AudioError(f"file exceeds {config.max_file_mb:g} MB limit")
    digest = hashlib.sha256()
    try:
        with open(path, "rb") as handle:
            while True:
                block = handle.read(_CHUNK)
                if not block:
                    break
                digest.update(block)
    except OSError as exc:
        raise AudioError(f"cannot read input: {exc.strerror}") from exc
    return digest.hexdigest()


def read_wav(path: Path, config: DetectionConfig) -> AudioData:
    try:
        stat = os.stat(path)
    except OSError as exc:
        raise AudioError(f"cannot stat input: {exc.strerror}") from exc
    limit = int(config.max_file_mb * 1024 * 1024)
    if stat.st_size > limit:
        raise AudioError(f"file exceeds {config.max_file_mb:g} MB limit")
    if stat.st_size < 12:
        raise AudioError("file is too small to be a WAV")
    try:
        handle = open(path, "rb")
    except OSError as exc:
        raise AudioError(f"cannot open input: {exc.strerror}") from exc
    with handle:
        header = _read_exact(handle, 12)
        if header[:4] != b"RIFF" or header[8:12] != b"WAVE":
            raise AudioError("not a RIFF/WAVE file")
        fmt: tuple[int, int, int, int] | None = None
        payload: bytes | None = None
        while True:
            chunk_header = handle.read(8)
            if not chunk_header:
                break
            if len(chunk_header) != 8:
                raise AudioError("truncated chunk header")
            chunk_id, chunk_size = struct.unpack("<4sI", chunk_header)
            padded = chunk_size + (chunk_size & 1)
            if chunk_id == b"fmt ":
                fmt = _parse_fmt(_read_exact(handle, chunk_size))
                if padded > chunk_size:
                    _read_exact(handle, padded - chunk_size)
            elif chunk_id == b"data":
                if fmt is None:
                    raise AudioError("data chunk precedes fmt chunk")
                payload = _read_exact(handle, chunk_size)
                if padded > chunk_size:
                    _read_exact(handle, padded - chunk_size)
                break
            else:
                handle.seek(padded, os.SEEK_CUR)
        if fmt is None:
            raise AudioError("missing fmt chunk")
        if payload is None:
            raise AudioError("missing data chunk")
    tag, channels, rate, bits = fmt
    try:
        config.validate_sample_rate(rate)
    except ConfigError as exc:
        raise AudioError(str(exc)) from exc
    samples = _decode(payload, tag, channels, bits)
    return AudioData(
        samples=samples,
        sample_rate=rate,
        channels=channels,
        source_bits=bits,
        frame_count=int(samples.size),
    )
