"""RIFF/WAVE chunk parsing.

Chunks are walked strictly by their declared sizes.  Unknown or extension
chunks (LIST, JUNK, bext, ...) are skipped while preserving RIFF's even-byte
padding rule.  A chunk whose payload runs past EOF marks the file as
truncated instead of silently accepting a short read.
"""

from __future__ import annotations

import struct
from dataclasses import dataclass

from .model import MediaError, SniffResult, Track

_FMT = struct.Struct("<HHIIHH")
_MAX_FMT_SIZE = 1024


@dataclass(slots=True)
class FmtChunk:
    format_tag: int
    channels: int
    sample_rate: int
    byte_rate: int
    block_align: int
    bits_per_sample: int


@dataclass(slots=True)
class WavInfo:
    fmt: FmtChunk | None
    data_size: int | None
    chunks: list[str]
    truncated: bool
    warnings: list[str]


def parse_wav(source) -> SniffResult:
    """Parse a WAV file from a path or a seekable binary file object."""
    from pathlib import Path

    if isinstance(source, (str, Path)):
        path = Path(source)
        with open(path, "rb") as handle:
            return parse_wav_stream(handle, file_size=path.stat().st_size)
    return parse_wav_stream(source)


def parse_wav_stream(handle, file_size: int | None = None) -> SniffResult:
    try:
        info = _scan_chunks(handle, file_size)
    except MediaError as exc:
        return SniffResult(
            container="wav", size=file_size or 0, error=str(exc)
        )
    chunks = info.chunks
    rounds = sum(1 for c in chunks if c == "data")
    extra = {
        "chunks": chunks,
        "fmt_format_tag": info.fmt.format_tag if info.fmt else None,
        "byte_rate": info.fmt.byte_rate if info.fmt else None,
        "rounds": rounds,
    }
    warnings = list(info.warnings)
    error: str | None = None
    if info.fmt is None:
        error = "missing fmt chunk"
    elif info.data_size is None:
        error = "missing data chunk"
    if rounds > 1:
        # Multiple data chunks are legal but ambiguous for a single track.
        warnings.append(f"{rounds} data chunks found; reporting only the first")

    tracks: list[Track] = []
    duration_ms: float | None = None
    if info.fmt is not None and info.data_size is not None:
        fmt = info.fmt
        if fmt.byte_rate > 0:
            duration_ms = round(info.data_size / fmt.byte_rate * 1000.0, 3)
        else:
            warnings.append("byte_rate is zero; duration unknown")
        tracks.append(
            Track(
                stable_index=1,
                kind="audio",
                language=None,
                codec=_codec_name(fmt),
                duration_ms=duration_ms,
                sample_rate=fmt.sample_rate,
                channels=fmt.channels,
                bit_depth=fmt.bits_per_sample,
                block_align=fmt.block_align,
                extra={
                    "format_tag": fmt.format_tag,
                    "byte_rate": fmt.byte_rate,
                    "data_size": info.data_size,
                },
            )
        )
    result = SniffResult(
        container="wav",
        size=file_size or _stream_size(handle),
        tracks=tracks,
        declared_duration_ms=duration_ms,
        warnings=warnings,
        error=error,
        truncated=info.truncated,
        metadata=extra,
    )
    return result


def _stream_size(handle) -> int:
    try:
        position = handle.tell()
        handle.seek(0, 2)
        size = handle.tell()
        handle.seek(position)
        return size
    except OSError:
        return 0


def _scan_chunks(handle, file_size: int | None) -> WavInfo:
    handle.seek(0)
    header = handle.read(12)
    if len(header) < 12:
        raise MediaError("file too short to hold a RIFF header", 0)
    riff, riff_size, wave = struct.unpack("<4sI4s", header)
    if riff != b"RIFF":
        raise MediaError("missing RIFF signature", 0)
    if wave != b"WAVE":
        raise MediaError("not a WAVE file (missing WAVE signature)", 8)

    actual_size = file_size if file_size is not None else _stream_size(handle)
    warnings: list[str] = []
    declared_end = 8 + riff_size
    truncated = False
    if declared_end > actual_size:
        truncated = True
        warnings.append(
            f"RIFF declares {riff_size} payload bytes but file has "
            f"{actual_size - 8} (offset 4)"
        )
    limit = min(actual_size, declared_end)

    chunks: list[str] = []
    fmt: FmtChunk | None = None
    data_size: int | None = None
    position = 12
    while position + 8 <= limit:
        handle.seek(position)
        raw = handle.read(8)
        if len(raw) < 8:
            truncated = True
            warnings.append(f"truncated chunk header (offset {position})")
            break
        chunk_id, chunk_size = struct.unpack("<4sI", raw)
        name = chunk_id.decode("latin-1")
        payload_start = position + 8
        payload_end = payload_start + chunk_size
        if payload_end > limit:
            truncated = True
            warnings.append(
                f"chunk {name!r} declares {chunk_size} bytes but only "
                f"{limit - payload_start} remain (offset {position})"
            )
            break
        chunks.append(name)
        if name == "fmt ":
            if fmt is not None:
                warnings.append(f"duplicate fmt chunk at offset {position}")
            elif chunk_size < 16:
                raise MediaError("fmt chunk smaller than 16 bytes", position)
            else:
                fmt = _read_fmt(handle, payload_start, chunk_size, position)
        elif name == "data" and data_size is None:
            data_size = chunk_size
        # Every other chunk is skipped by its declared size.  An odd size
        # gains one padding byte, which is not itself a chunk.
        position = payload_end + (chunk_size & 1)
    else:
        if position < limit and limit - position > 0:
            # Fewer than 8 trailing bytes that are not a padding byte.
            if limit - position > 1:
                warnings.append(
                    f"trailing {limit - position} byte(s) after last chunk"
                )
    if data_size is None and "data" not in chunks:
        warnings.append("no data chunk present")
    return WavInfo(
        fmt=fmt,
        data_size=data_size,
        chunks=chunks,
        truncated=truncated,
        warnings=warnings,
    )


def _read_fmt(handle, payload_start: int, chunk_size: int, offset: int) -> FmtChunk:
    if chunk_size > _MAX_FMT_SIZE:
        raise MediaError("fmt chunk suspiciously large", offset)
    handle.seek(payload_start)
    raw = handle.read(min(chunk_size, _FMT.size))
    if len(raw) < _FMT.size:
        raise MediaError("truncated fmt chunk", offset)
    tag, channels, rate, byte_rate, align, bits = _FMT.unpack(raw)
    return FmtChunk(
        format_tag=tag,
        channels=channels,
        sample_rate=rate,
        byte_rate=byte_rate,
        block_align=align,
        bits_per_sample=bits,
    )


def _codec_name(fmt: FmtChunk) -> str:
    if fmt.format_tag == 1:
        return f"pcm_s{fmt.bits_per_sample}le"
    if fmt.format_tag == 3:
        return f"pcm_f{fmt.bits_per_sample}le"
    if fmt.format_tag == 0xFFFE:
        return f"pcm_extensible_s{fmt.bits_per_sample}"
    if fmt.format_tag == 6:
        return "alaw"
    if fmt.format_tag == 7:
        return "mulaw"
    return f"tag_0x{fmt.format_tag:04x}"
