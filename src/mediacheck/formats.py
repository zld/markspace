"""Low-level container structure readers: ISO base media (MP4) boxes and
Matroska EBML elements.

Only structural metadata is parsed.  All reads are bounded by explicit
element ranges so a corrupt header cannot trigger unbounded allocation,
recursion is capped, and no payload bytes are ever decoded as code.
"""

from __future__ import annotations

import struct
from dataclasses import dataclass

# A single media file must not exceed 2 GiB and parsing must stay well below
# the 64 MiB memory budget; reads are therefore small and strictly bounded.
MAX_FILE_SIZE = 2 * 1024 * 1024 * 1024
MAX_BOX_DEPTH = 16
_READ = 8


class ContainerError(Exception):
    """Raised on malformed container structure (bad header, out-of-range
    element, integer overflow, excessive nesting, ...)."""


@dataclass(slots=True)
class Box:
    """One MP4 box located inside an open binary file."""

    f: object
    kind: bytes
    start: int          # offset of the size field
    header: int         # offset of the first payload byte
    end: int            # exclusive end offset

    def payload(self) -> bytes:
        """Read the full box payload.  Callers only invoke this on small
        metadata boxes, never on media data boxes."""
        length = self.end - self.header
        if length < 0 or length > 64 * 1024 * 1024:
            raise ContainerError(f"box {self.kind!r} payload out of bounds")
        self.f.seek(self.header)
        data = self.f.read(length)
        if len(data) != length:
            raise ContainerError(f"truncated box {self.kind!r} payload")
        return data


# Boxes whose payload is itself a sequence of child boxes.
_CONTAINER_BOXES = {
    b"moov", b"trak", b"mdia", b"minf", b"stbl", b"edts",
    b"mvex", b"moof", b"traf", b"mfra", b"udta", b"dinf",
}
# 'meta' is a FullBox (version+flags prefix) followed by child boxes.
_FULL_CONTAINER_BOXES = {b"meta"}


def iter_boxes(f, start: int, end: int, depth: int = 0):
    """Yield every Box strictly inside the [start, end) range."""
    if depth > MAX_BOX_DEPTH:
        raise ContainerError("box nesting deeper than 16 levels")
    pos = start
    while pos < end:
        if end - pos < 8:
            raise ContainerError("truncated box header")
        f.seek(pos)
        header = f.read(8)
        if len(header) != 8:
            raise ContainerError("truncated box header")
        size32, kind = struct.unpack(">I4s", header)
        payload_start = pos + 8
        if size32 == 1:  # 64-bit extended size
            ext = f.read(8)
            if len(ext) != 8:
                raise ContainerError("truncated 64-bit box size")
            size = struct.unpack(">Q", ext)[0]
            payload_start += 8
        elif size32 == 0:  # box extends to end of enclosing range/file
            size = end - pos
        else:
            size = size32
        if size < payload_start - pos or size > MAX_FILE_SIZE:
            raise ContainerError(f"invalid box size for {kind!r}")
        box_end = pos + size
        if box_end > end:
            raise ContainerError(f"box {kind!r} extends past enclosing range")
        box = Box(f, kind, pos, payload_start, box_end)
        yield box
        if kind in _CONTAINER_BOXES:
            yield from iter_boxes(f, payload_start, box_end, depth + 1)
        elif kind in _FULL_CONTAINER_BOXES:
            if box_end - payload_start < 4:
                raise ContainerError("truncated full-box header")
            yield from iter_boxes(f, payload_start + 4, box_end, depth + 1)
        pos = box_end


def find_boxes(f, start: int, end: int, wanted: bytes):
    """Direct children (any nesting depth reached via iter_boxes) of a
    given kind inside [start, end)."""
    return [b for b in iter_boxes(f, start, end) if b.kind == wanted]


def _u16(data: bytes, off: int) -> int:
    if off + 2 > len(data):
        raise ContainerError("field read past box payload")
    return struct.unpack_from(">H", data, off)[0]


def _u32(data: bytes, off: int) -> int:
    if off + 4 > len(data):
        raise ContainerError("field read past box payload")
    return struct.unpack_from(">I", data, off)[0]


def _parse_mdhd(payload: bytes) -> tuple[int | None, int | None]:
    if len(payload) < 4:
        raise ContainerError("truncated mdhd")
    version = payload[0]
    if version == 1:
        if len(payload) < 32:
            raise ContainerError("truncated mdhd v1")
        timescale = _u32(payload, 20)
        duration = struct.unpack_from(">Q", payload, 24)[0]
    else:
        if len(payload) < 20:
            raise ContainerError("truncated mdhd v0")
        timescale = _u32(payload, 12)
        duration = _u32(payload, 16)
    return timescale, duration


def _parse_tkhd(payload: bytes) -> int | None:
    if len(payload) < 4:
        raise ContainerError("truncated tkhd")
    version = payload[0]
    off = 4 if version == 0 else 8
    if version not in (0, 1) or len(payload) < off + 8:
        raise ContainerError("unsupported tkhd version")
    # track_ID is the second integer after creation/modification time.
    return _u32(payload, off + 4)


_HANDLER_TYPES = {b"vide": "video", b"soun": "audio", b"subt": "subtitle",
                  b"text": "subtitle"}


def _parse_hdlr(payload: bytes) -> str | None:
    if len(payload) < 12:
        raise ContainerError("truncated hdlr")
    handler = payload[8:12]
    return _HANDLER_TYPES.get(handler)


def _parse_stsd(payload: bytes) -> dict | None:
    if len(payload) < 8:
        raise ContainerError("truncated stsd")
    entry_count = _u32(payload, 4)
    if entry_count == 0:
        return None
    if entry_count != 1 or len(payload) < 16:
        raise ContainerError("unsupported stsd entry layout")
    size = _u32(payload, 8)
    if size < 8 or 8 + size > len(payload):
        raise ContainerError("stsd entry out of bounds")
    codec = payload[12:16]
    body = payload[16:8 + size]
    info: dict = {"codec": codec.decode("latin-1")}
    if len(body) >= 18:
        info["width"] = _u16(body, 8)
        info["height"] = _u16(body, 10)
    if len(body) >= 28:
        info["sample_rate"] = _u16(body, 16)
        info["channels"] = _u16(body, 20)
    return info


def parse_mp4(f, file_size: int) -> dict:
    """Parse an open MP4 file and return raw, codec-neutral metadata."""
    if file_size > MAX_FILE_SIZE:
        raise ContainerError("file exceeds 2 GiB limit")
    f.seek(0)
    brand = f.read(8)
    if len(brand) < 8 or brand[4:8] != b"ftyp":
        raise ContainerError("missing ftyp box")
    result: dict = {"container": "mp4", "duration_ms": None, "tracks": []}
    for box in iter_boxes(f, 0, file_size):
        if box.kind != b"moov":
            continue
        for mvhd in find_boxes(f, box.header, box.end, b"mvhd"):
            payload = mvhd.payload()
            if len(payload) >= 20 and payload[0] == 0:
                timescale = _u32(payload, 12)
                duration = _u32(payload, 16)
                if timescale:
                    result["duration_ms"] = round(duration * 1000 / timescale)
        for trak in find_boxes(f, box.header, box.end, b"trak"):
            track: dict = {"track_id": None, "type": None, "codec": None,
                           "duration_ms": None, "width": None,
                           "height": None, "sample_rate": None,
                           "channels": None}
            for tkhd in find_boxes(f, trak.header, trak.end, b"tkhd"):
                track["track_id"] = _parse_tkhd(tkhd.payload())
            for mdia in find_boxes(f, trak.header, trak.end, b"mdia"):
                for mdhd in find_boxes(f, mdia.header, mdia.end, b"mdhd"):
                    timescale, duration = _parse_mdhd(mdhd.payload())
                    if timescale and duration is not None:
                        track["duration_ms"] = round(duration * 1000 / timescale)
                for hdlr in find_boxes(f, mdia.header, mdia.end, b"hdlr"):
                    track["type"] = _parse_hdlr(hdlr.payload())
                for stsd in find_boxes(f, mdia.header, mdia.end, b"stsd"):
                    entry = _parse_stsd(stsd.payload())
                    if entry:
                        track.update(entry)
            result["tracks"].append(track)
        break  # only the first moov is authoritative
    return result
