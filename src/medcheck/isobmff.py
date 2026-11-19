"""Minimal ISO BMFF / QuickTime MOV box parser.

Walks ftyp/moov boxes, extracting per-track type, timescale, duration,
language, codec FourCC and audio/video parameters. Unknown boxes are
skipped safely. Oversized declarations, parent-boundary violations and
excessive nesting abort parsing of the file with a structural error.
Error messages never include file content.
"""

from __future__ import annotations

import os
import struct
from dataclasses import dataclass, field

from . import MAX_BOX_DEPTH, MAX_FILE_BYTES

MAX_MOOV_BYTES = 256 * 1024 ** 2  # metadata larger than this is rejected

_HANDLER_KINDS = {
    b"vide": "video",
    b"soun": "audio",
    b"text": "text",
    b"sbtl": "text",
    b"hint": "hint",
}

_VIDEO_CODECS = {b"avc1", b"avc3", b"hev1", b"hvc1", b"mp4v", b"jpeg",
                 b"png ", b"vp09", b"av01", b"dvh1", b"dvhe"}
_AUDIO_CODECS = {b"mp4a", b"alac", b"ac-3", b"ec-3", b"Opus", b"fLaC",
                 b"sowt", b"twos", b"lpcm"}


class IsoBmffError(Exception):
    """Fatal structural problem; parsing of this file aborts."""


@dataclass
class TrackInfo:
    kind: str = "other"
    codec: str = "unknown"
    timescale: int | None = None
    duration_ms: float | None = None
    declared_duration_ms: float | None = None
    language: str | None = None
    sample_rate: int | None = None
    channels: int | None = None
    width: int | None = None
    height: int | None = None


@dataclass
class MovInfo:
    container: str = "mov"
    major_brand: str | None = None
    timescale: int | None = None
    duration_ms: float | None = None
    tracks: list[TrackInfo] = field(default_factory=list)
    errors: list[str] = field(default_factory=list)


def _box_type_name(btype: bytes) -> str:
    return "".join(chr(b) if 32 <= b < 127 else "?" for b in btype)


def _box_header(data: bytes, pos: int, end: int) -> tuple[bytes, int, int]:
    """Return (type, body_offset, box_end) for the box at pos."""
    if pos + 8 > end:
        raise IsoBmffError(f"truncated box header at offset {pos}")
    size, btype = struct.unpack_from(">I4s", data, pos)
    header = 8
    if size == 1:
        if pos + 16 > end:
            raise IsoBmffError(f"truncated extended-size box header at offset {pos}")
        size = struct.unpack_from(">Q", data, pos + 8)[0]
        header = 16
    elif size == 0:
        size = end - pos  # box extends to end of parent
    if size < header:
        raise IsoBmffError(
            f"box {_box_type_name(btype)} at offset {pos} declares invalid size {size}")
    if pos + size > end:
        raise IsoBmffError(
            f"box {_box_type_name(btype)} at offset {pos} declares size {size} "
            f"beyond parent boundary {end}")
    return btype, pos + header, pos + size


def _decode_language(bits: int) -> str | None:
    if bits == 0:
        return None
    chars = []
    for shift in (10, 5, 0):
        c = ((bits >> shift) & 0x1F) + 0x60
        if not 0x61 <= c <= 0x7A:
            return None
        chars.append(chr(c))
    return "".join(chars)


def _parse_mvhd(p: bytes, info: MovInfo) -> None:
    version = p[0]
    if version == 1 and len(p) >= 32:
        info.timescale = struct.unpack_from(">I", p, 20)[0]
        duration = struct.unpack_from(">Q", p, 24)[0]
    elif len(p) >= 20:
        info.timescale, duration = struct.unpack_from(">II", p, 12)
    else:
        return
    if info.timescale:
        info.duration_ms = duration * 1000.0 / info.timescale


def _parse_tkhd(p: bytes, track: TrackInfo, movie_timescale: int | None) -> None:
    version = p[0]
    if version == 1 and len(p) >= 36:
        duration = struct.unpack_from(">Q", p, 28)[0]
    elif len(p) >= 24:
        duration = struct.unpack_from(">I", p, 20)[0]
    else:
        return
    if movie_timescale:
        track.declared_duration_ms = duration * 1000.0 / movie_timescale


def _parse_mdhd(p: bytes, track: TrackInfo) -> None:
    version = p[0]
    if version == 1 and len(p) >= 34:
        track.timescale = struct.unpack_from(">I", p, 20)[0]
        duration = struct.unpack_from(">Q", p, 24)[0]
        lang_bits = struct.unpack_from(">H", p, 32)[0]
    elif len(p) >= 22:
        track.timescale, duration = struct.unpack_from(">II", p, 12)
        lang_bits = struct.unpack_from(">H", p, 20)[0]
    else:
        return
    if track.timescale:
        track.duration_ms = duration * 1000.0 / track.timescale
    track.language = _decode_language(lang_bits)


def _parse_stsd(p: bytes, track: TrackInfo) -> None:
    if len(p) < 16:
        return
    pos = 8  # skip version/flags and entry_count; only the first entry is read
    size, fmt = struct.unpack_from(">I4s", p, pos)
    if size < 8 or pos + size > len(p):
        raise IsoBmffError("stsd sample entry exceeds its box")
    track.codec = _box_type_name(fmt)
    entry = p[pos + 8:pos + size]
    if fmt in _VIDEO_CODECS and len(entry) >= 28:
        track.width, track.height = struct.unpack_from(">HH", entry, 24)
    elif fmt in _AUDIO_CODECS and len(entry) >= 28:
        track.channels = struct.unpack_from(">H", entry, 16)[0]
        rate_fixed = struct.unpack_from(">I", entry, 24)[0]
        track.sample_rate = rate_fixed >> 16


def _walk(data: bytes, start: int, end: int, depth: int, info: MovInfo,
          track: TrackInfo | None) -> None:
    if depth > MAX_BOX_DEPTH:
        raise IsoBmffError(f"box nesting exceeds depth {MAX_BOX_DEPTH}")
    pos = start
    while pos < end:
        btype, body, box_end = _box_header(data, pos, end)
        payload = data[body:box_end]
        if btype == b"mvhd":
            _parse_mvhd(payload, info)
        elif btype == b"trak":
            child = TrackInfo()
            _walk(data, body, box_end, depth + 1, info, child)
            info.tracks.append(child)
        elif track is not None and btype == b"tkhd":
            _parse_tkhd(payload, track, info.timescale)
        elif track is not None and btype == b"mdhd":
            _parse_mdhd(payload, track)
        elif track is not None and btype == b"hdlr" and len(payload) >= 12:
            handler = payload[8:12]
            track.kind = _HANDLER_KINDS.get(handler, "other")
        elif track is not None and btype == b"stsd":
            _parse_stsd(payload, track)
        elif btype in (b"mdia", b"minf", b"stbl", b"edts", b"dinf", b"udta"):
            _walk(data, body, box_end, depth + 1, info, track)
        # Unknown boxes are skipped.
        pos = box_end


def parse_mov(path: str) -> MovInfo:
    """Parse an ISO BMFF / MOV file, collecting structural errors."""
    info = MovInfo()
    size = os.path.getsize(path)
    if size > MAX_FILE_BYTES:
        info.errors.append(f"file exceeds 2 GiB limit ({size} bytes)")
        return info
    try:
        with open(path, "rb") as fh:
            pos = 0
            while pos + 8 <= size:
                fh.seek(pos)
                hdr = fh.read(16)
                if len(hdr) < 8:
                    raise IsoBmffError(f"truncated box header at offset {pos}")
                bsize, btype = struct.unpack_from(">I4s", hdr, 0)
                header = 8
                if bsize == 1:
                    bsize = struct.unpack_from(">Q", hdr, 8)[0]
                    header = 16
                elif bsize == 0:
                    bsize = size - pos
                if bsize < header or pos + bsize > size:
                    raise IsoBmffError(
                        f"box {_box_type_name(btype)} at offset {pos} declares "
                        f"size {bsize} beyond file size {size}")
                if btype == b"ftyp":
                    brand = fh.read(4)
                    info.major_brand = _box_type_name(brand)
                elif btype == b"moov":
                    if bsize - header > MAX_MOOV_BYTES:
                        raise IsoBmffError(
                            f"moov box too large ({bsize - header} bytes)")
                    fh.seek(pos + header)
                    _walk(fh.read(bsize - header), 0, bsize - header, 1, info, None)
                pos += bsize
    except IsoBmffError as exc:
        info.errors.append(str(exc))
        info.tracks.clear()
    if not info.errors and not info.tracks:
        info.errors.append("no tracks found (missing or empty moov box)")
    return info
