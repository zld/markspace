"""Minimal RIFF/WAVE and RF64 chunk parser.

Extracts PCM/float format parameters, sample rate, bit depth, channel count
and data length. Unknown chunks are skipped. Out-of-bounds chunk lengths,
truncated chunks and conflicting fmt chunks are reported as structural
errors. Error messages never include file content.
"""

from __future__ import annotations

import os
import struct
from dataclasses import dataclass, field

from . import MAX_FILE_BYTES

_FMT = b"fmt "
_DATA = b"data"
_DS64 = b"ds64"

_AUDIO_CODECS = {1: "pcm", 3: "float", 0xFFFE: "extensible"}


@dataclass
class WavInfo:
    container: str = "wav"
    audio_format: int | None = None
    sample_rate: int | None = None
    bit_depth: int | None = None
    channels: int | None = None
    byte_rate: int | None = None
    data_bytes: int | None = None
    duration_ms: float | None = None
    errors: list[str] = field(default_factory=list)

    @property
    def codec(self) -> str:
        if self.audio_format is None:
            return "unknown"
        return _AUDIO_CODECS.get(self.audio_format, f"tag{self.audio_format}")


def _chunk_name(cid: bytes) -> str:
    return "".join(chr(b) if 32 <= b < 127 else "?" for b in cid)


def parse_wav(path: str) -> WavInfo:
    """Parse a RIFF/RF64 WAVE file, collecting structural errors."""
    info = WavInfo()
    size = os.path.getsize(path)
    if size > MAX_FILE_BYTES:
        info.errors.append(f"file exceeds 2 GiB limit ({size} bytes)")
        return info
    with open(path, "rb") as fh:
        header = fh.read(12)
        if len(header) < 12 or header[:4] not in (b"RIFF", b"RF64") or header[8:12] != b"WAVE":
            info.errors.append("not a RIFF/RF64 WAVE file")
            return info
        rf64 = header[:4] == b"RF64"
        ds64_sizes: tuple[int, ...] | None = None
        fmt_seen: tuple[int, ...] | None = None
        offset = 12
        while offset + 8 <= size:
            fh.seek(offset)
            hdr = fh.read(8)
            if len(hdr) < 8:
                info.errors.append(f"truncated chunk header at offset {offset}")
                break
            cid = hdr[:4]
            csize = struct.unpack("<I", hdr[4:8])[0]
            if rf64 and csize == 0xFFFFFFFF:
                if cid == _DATA and ds64_sizes is not None:
                    csize = ds64_sizes[1]
                else:
                    info.errors.append(
                        f"chunk {_chunk_name(cid)} uses RF64 placeholder size without ds64"
                    )
                    break
            body = offset + 8
            end = body + csize
            if end > size:
                info.errors.append(
                    f"chunk {_chunk_name(cid)} out of bounds: declares {csize} bytes "
                    f"at offset {body}, file is {size} bytes"
                )
                break
            if cid == _DS64:
                data = fh.read(min(csize, 28))
                if len(data) >= 28:
                    ds64_sizes = struct.unpack("<QQQI", data[:28])
            elif cid == _FMT:
                data = fh.read(min(csize, 40))
                if len(data) < 16:
                    info.errors.append("fmt chunk too short")
                else:
                    fmt = struct.unpack("<HHIIHH", data[:16])
                    if fmt_seen is not None and fmt_seen != fmt:
                        info.errors.append("conflicting fmt chunks")
                    else:
                        fmt_seen = fmt
                        (info.audio_format, info.channels, info.sample_rate,
                         info.byte_rate, _block_align, info.bit_depth) = fmt
            elif cid == _DATA:
                if info.data_bytes is not None:
                    info.errors.append("multiple data chunks")
                else:
                    info.data_bytes = csize
            # Unknown chunks are skipped by advancing past them.
            offset = end + (csize & 1)  # chunks are padded to even sizes
        if fmt_seen is None:
            info.errors.append("missing fmt chunk")
        if info.data_bytes is None:
            info.errors.append("missing data chunk")
    if info.byte_rate and info.data_bytes is not None:
        info.duration_ms = info.data_bytes * 1000.0 / info.byte_rate
    return info
