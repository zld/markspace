"""RIFF/RF64 WAVE and FORM/AIFF chunk parsing.

Only structural facts are returned.  Missing fields stay ``None`` - nothing
is guessed from extensions or neighbouring chunks.
"""

from __future__ import annotations

import os
import struct
from dataclasses import dataclass, field
from typing import BinaryIO

from .errors import ERR_IO, ERR_TRUNCATED, ScanFailure
from .limits import Limits

_WAVE_TAGS = {
    0x0001: "pcm_s16le",  # refined by bit depth below
    0x0003: "pcm_float",
    0x0006: "alaw",
    0x0007: "mulaw",
    0x0011: "ima_adpcm",
    0x0055: "mpeg_layer3",
    0xFFFE: "pcm_extensible",
}


def _read_at(fh: BinaryIO, offset: int, n: int) -> bytes:
    fh.seek(offset)
    data = fh.read(n)
    if len(data) != n:
        raise ScanFailure(
            ERR_TRUNCATED,
            f"expected {n} bytes at offset {offset}, got {len(data)}",
            offset,
        )
    return data


def _open_size(path: str, limits: Limits) -> tuple[BinaryIO, int]:
    try:
        size = os.path.getsize(path)
    except OSError as exc:
        raise ScanFailure(ERR_IO, f"cannot stat file: {exc}", 0) from exc
    limits.check_file_size(size)
    try:
        fh = open(path, "rb")
    except OSError as exc:
        raise ScanFailure(ERR_IO, f"cannot open file: {exc}", 0) from exc
    return fh, size


@dataclass
class Chunk:
    type: str
    offset: int
    size: int


@dataclass
class RiffData:
    kind: str  # "wav", "rf64" or "aiff"
    chunks: list[Chunk] = field(default_factory=list)
    chunk_index: dict[str, Chunk] = field(default_factory=dict)
    sample_rate: int | None = None
    channels: int | None = None
    bit_depth: int | None = None
    codec: str | None = None
    frames: int | None = None
    container_duration_ms: int | None = None
    unknown_fields: list[str] = field(default_factory=list)
    rf64_data_size: int | None = None


def _add_unknown(data: RiffData, name: str) -> None:
    if name not in data.unknown_fields:
        data.unknown_fields.append(name)


def _pcm_codec(tag: int, bits: int | None) -> str:
    if tag == 0x0001:
        if bits:
            return f"pcm_s{bits}le"
        return "pcm_le"
    if tag == 0x0003:
        if bits:
            return f"pcm_f{bits}le"
        return "pcm_float_le"
    return _WAVE_TAGS.get(tag, f"tag_0x{tag:04x}")


def parse_wav(path: str, limits: Limits | None = None) -> RiffData:
    limits = limits or Limits()
    fh, file_size = _open_size(path, limits)
    try:
        header = _read_at(fh, 0, 12)
        magic = header[0:4]
        if magic not in (b"RIFF", b"RF64"):
            raise ScanFailure(ERR_TRUNCATED, "missing RIFF/RF64 signature", 0)
        if header[8:12] != b"WAVE":
            raise ScanFailure(ERR_TRUNCATED, "RIFF form is not WAVE", 8)
        kind = "rf64" if magic == b"RF64" else "wav"
        declared = struct.unpack("<I", header[4:8])[0]
        end = file_size if kind == "rf64" else 8 + declared
        if end > file_size:
            raise ScanFailure(
                ERR_TRUNCATED,
                f"RIFF declares {end} bytes, file has {file_size}",
                4,
            )
        data = RiffData(kind=kind)
        _walk_riff(fh, 12, end, data, limits)
        _read_wav_fields(fh, data)
        return data
    finally:
        fh.close()


def _walk_riff(
    fh: BinaryIO, start: int, end: int, data: RiffData, limits: Limits
) -> None:
    pos = start
    count = 0
    ds64_raw: bytes | None = None
    while pos + 8 <= end:
        head = _read_at(fh, pos, 8)
        cid = head[0:4].decode("latin-1")
        size = struct.unpack("<I", head[4:8])[0]
        count += 1
        if count > limits.max_boxes:
            raise ScanFailure(
                ERR_TRUNCATED, f"more than {limits.max_boxes} RIFF chunks", pos
            )
        payload = pos + 8
        if payload + size > end:
            raise ScanFailure(
                ERR_TRUNCATED,
                f"{cid!r} chunk size {size} exceeds RIFF boundary",
                pos,
            )
        chunk = Chunk(cid, pos, size)
        data.chunks.append(chunk)
        data.chunk_index.setdefault(cid, chunk)
        if cid == "ds64" and ds64_raw is None:
            if size < 28:
                raise ScanFailure(ERR_TRUNCATED, "ds64 chunk is too small", pos)
            ds64_raw = _read_at(fh, payload, 28)
        next_pos = payload + size
        if size % 2:
            next_pos += 1
        pos = next_pos
    if pos < end:
        raise ScanFailure(
            ERR_TRUNCATED, f"{end - pos} trailing bytes cannot hold a chunk header", pos
        )
    data.rf64_data_size = None
    if data.kind == "rf64":
        if ds64_raw is None:
            raise ScanFailure(ERR_TRUNCATED, "RF64 file has no ds64 chunk", 12)
        _riff_size, data_size, sample_count = struct.unpack("<QQQ", ds64_raw[4:28])
        chunk = data.chunk_index.get("data")
        if chunk is not None and chunk.size == 0xFFFFFFFF:
            data.frames = sample_count or None
            data.rf64_data_size = data_size


def _read_wav_fields(fh: BinaryIO, data: RiffData) -> None:
    fmt = data.chunk_index.get("fmt ")
    if fmt is None:
        for name in ("codec", "sample_rate", "channels", "bit_depth", "duration_ms"):
            _add_unknown(data, name)
        return
    if fmt.size < 16:
        raise ScanFailure(
            ERR_TRUNCATED, "fmt chunk is smaller than 16 bytes", fmt.offset
        )
    raw = _read_at(fh, fmt.offset + 8, min(fmt.size, 40))
    tag, channels, rate, byte_rate, block_align, bits = struct.unpack(
        "<HHIIHH", raw[0:16]
    )
    if tag == 0xFFFE:
        if len(raw) < 26:
            raise ScanFailure(
                ERR_TRUNCATED, "extensible fmt chunk is truncated", fmt.offset
            )
        sub = struct.unpack("<H", raw[24:26])[0]
        tag = sub
    data.codec = _pcm_codec(tag, bits or None)
    data.channels = channels or None
    data.sample_rate = rate or None
    data.bit_depth = bits or None
    if not data.channels:
        _add_unknown(data, "channels")
    if not data.sample_rate:
        _add_unknown(data, "sample_rate")
    if not data.bit_depth:
        _add_unknown(data, "bit_depth")
    data_chunk = data.chunk_index.get("data")
    if data.frames is None and data_chunk is not None:
        if data_chunk.size == 0xFFFFFFFF and data.rf64_data_size is not None:
            size = data.rf64_data_size
        else:
            size = data_chunk.size
        if block_align:
            data.frames = size // block_align
        else:
            _add_unknown(data, "frames")
    if data.frames is None:
        _add_unknown(data, "duration_ms")
        return
    if data.sample_rate:
        data.container_duration_ms = round(data.frames * 1000 / data.sample_rate)
    else:
        _add_unknown(data, "duration_ms")


def parse_aiff(path: str, limits: Limits | None = None) -> RiffData:
    limits = limits or Limits()
    fh, file_size = _open_size(path, limits)
    try:
        header = _read_at(fh, 0, 12)
        if header[0:4] != b"FORM":
            raise ScanFailure(ERR_TRUNCATED, "missing FORM signature", 0)
        form = header[8:12]
        if form not in (b"AIFF", b"AIFC"):
            raise ScanFailure(ERR_TRUNCATED, "FORM is not AIFF/AIFC", 8)
        declared = struct.unpack(">I", header[4:8])[0]
        end = 8 + declared
        if end > file_size:
            raise ScanFailure(
                ERR_TRUNCATED, f"FORM declares {end} bytes, file has {file_size}", 4
            )
        data = RiffData(kind="aiff")
        _walk_aiff(fh, 12, end, data, limits)
        _read_aiff_fields(fh, data)
        return data
    finally:
        fh.close()


def _walk_aiff(
    fh: BinaryIO, start: int, end: int, data: RiffData, limits: Limits
) -> None:
    pos = start
    count = 0
    while pos + 8 <= end:
        head = _read_at(fh, pos, 8)
        cid = head[0:4].decode("latin-1")
        size = struct.unpack(">I", head[4:8])[0]
        count += 1
        if count > limits.max_boxes:
            raise ScanFailure(
                ERR_TRUNCATED, f"more than {limits.max_boxes} AIFF chunks", pos
            )
        payload = pos + 8
        if payload + size > end:
            raise ScanFailure(
                ERR_TRUNCATED,
                f"{cid!r} chunk size {size} exceeds FORM boundary",
                pos,
            )
        chunk = Chunk(cid, pos, size)
        data.chunks.append(chunk)
        data.chunk_index.setdefault(cid, chunk)
        pos = payload + size
        if size % 2:
            pos += 1
    if pos < end:
        raise ScanFailure(
            ERR_TRUNCATED, f"{end - pos} trailing bytes cannot hold a chunk header", pos
        )


def _read_aiff_fields(fh: BinaryIO, data: RiffData) -> None:
    comm = data.chunk_index.get("COMM")
    if comm is None:
        for name in ("codec", "sample_rate", "channels", "bit_depth", "duration_ms"):
            _add_unknown(data, name)
        return
    if comm.size < 18:
        raise ScanFailure(
            ERR_TRUNCATED, "COMM chunk is smaller than 18 bytes", comm.offset
        )
    raw = _read_at(fh, comm.offset + 8, min(comm.size, 40))
    channels, frames, bits = struct.unpack(">HIH", raw[0:8])
    rate = _decode_extended(raw[8:18])
    codec = f"pcm_s{bits}be" if bits else "pcm_be"
    if data.kind == "aiff" and len(raw) >= 22:
        compression = raw[18:22]
        if compression not in (b"\x00\x00\x00\x00", b"NONE"):
            codec = compression.decode("latin-1").strip("\x00") or codec
    data.channels = channels or None
    data.bit_depth = bits or None
    data.sample_rate = int(rate) if rate and rate > 0 else None
    data.codec = codec
    data.frames = frames
    if not data.channels:
        _add_unknown(data, "channels")
    if not data.bit_depth:
        _add_unknown(data, "bit_depth")
    if not data.sample_rate:
        _add_unknown(data, "sample_rate")
        _add_unknown(data, "duration_ms")
    else:
        data.container_duration_ms = round(frames * 1000 / data.sample_rate)


def _decode_extended(raw: bytes) -> float:
    if len(raw) != 10:
        raise ScanFailure(ERR_TRUNCATED, "80-bit sample rate is truncated", 0)
    sign_exp = struct.unpack(">H", raw[0:2])[0]
    mantissa = struct.unpack(">Q", raw[2:10])[0]
    sign = -1.0 if sign_exp & 0x8000 else 1.0
    exponent = sign_exp & 0x7FFF
    if exponent == 0 and mantissa == 0:
        return 0.0
    if exponent == 0x7FFF:
        return float("nan")
    return sign * mantissa * (2.0 ** (exponent - 16383 - 63))


def encode_extended(value: float) -> bytes:
    """Encode a positive float as an 80-bit IEEE 754 extended value (AIFF)."""
    if value <= 0:
        return b"\x00" * 10
    mantissa, exponent = _frexp(value)
    sign_exp = exponent + 16383
    return struct.pack(">HQ", sign_exp, mantissa)


def _frexp(value: float) -> tuple[int, int]:
    import math

    mant, exp = math.frexp(value)
    return int(mant * (1 << 63)), exp
