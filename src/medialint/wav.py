"""RIFF/WAVE extractor: PCM format, sample rate, channels, sample count."""
from __future__ import annotations

import struct

from .model import CodecParams, Inconsistency, MediaError, TrackInfo

MAX_CHUNKS = 1024


def parse_wav(data: bytes, rel: str) -> tuple[list[TrackInfo], list[Inconsistency]]:
    """Parse a RIFF/WAVE buffer; returns (tracks, per-track problems)."""
    if len(data) < 12:
        raise MediaError("truncated-chunk", f"{rel}: file too small for a RIFF header", 0)
    riff_size = struct.unpack_from("<I", data, 4)[0]
    if riff_size + 8 > len(data):
        raise MediaError(
            "truncated-chunk",
            f"{rel}: RIFF header declares {riff_size + 8} bytes but file has {len(data)}",
            4,
        )
    end = min(len(data), riff_size + 8)
    fmt: tuple[int, int, int, int] | None = None  # format, channels, rate, block_align
    data_size: int | None = None
    pos = 12
    n = 0
    while pos + 8 <= end:
        cid, size = struct.unpack_from("<4sI", data, pos)
        if pos + 8 + size > end:
            raise MediaError(
                "truncated-chunk",
                f"{rel}: chunk {cid.decode('latin1')!r} at offset {pos} declares {size} bytes, file ends early",
                pos,
            )
        if cid == b"fmt ":
            if size < 16:
                raise MediaError("invalid-chunk", f"{rel}: fmt chunk too small ({size} bytes)", pos)
            audio_format, channels, rate, _byte_rate, block_align, _bits = struct.unpack_from(
                "<HHIIHH", data, pos + 8
            )
            fmt = (audio_format, channels, rate, block_align)
        elif cid == b"data":
            data_size = size
        pos += 8 + size + (size & 1)
        n += 1
        if n > MAX_CHUNKS:
            raise MediaError("limit-exceeded", f"{rel}: more than {MAX_CHUNKS} RIFF chunks", pos)
    if fmt is None:
        raise MediaError("missing-chunk", f"{rel}: no fmt chunk found")
    if data_size is None:
        raise MediaError("missing-chunk", f"{rel}: no data chunk found")

    audio_format, channels, rate, block_align = fmt
    if audio_format != 1:
        problem = Inconsistency(
            code="unsupported-codec",
            path=rel,
            track="fmt",
            message=f"{rel}: WAVE format tag {audio_format} is not PCM (1); only PCM is supported",
        )
        return [], [problem]
    if rate <= 0 or channels <= 0 or block_align <= 0:
        raise MediaError("invalid-chunk", f"{rel}: degenerate fmt values (rate={rate}, channels={channels}, align={block_align})")
    samples = data_size // block_align
    duration_ms = (samples * 1000 + rate // 2) // rate
    track = TrackInfo(
        track_id=1,
        kind="audio",
        codec=CodecParams(codec="pcm", sample_rate=rate, channels=channels),
        duration_ms=duration_ms,
        timescale=rate,
        locator="fmt/data",
    )
    return [track], []
