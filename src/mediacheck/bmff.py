"""Generic ISO BMFF box iteration and leaf-field decoders.

This module knows about box framing and the few header layouts mediacheck
needs; it deliberately does not build tracks.  Every length is validated
against the enclosing reader before use.
"""

from __future__ import annotations

from typing import Any, Iterator

from .binary import BinaryError, Reader, checked_box_end
from .model import (
    KIND_AUDIO,
    KIND_SUBTITLE,
    KIND_VIDEO,
    Issue,
    MediaFile,
    SEVERITY_ERROR,
    SEVERITY_WARNING,
)

FTYP = 0x66747970
MOOV = 0x6D6F6F76
MVHD = 0x6D766864
TRAK = 0x7472616B
TKHD = 0x746B6864
MDIA = 0x6D646961
MDHD = 0x6D646864
HDLR = 0x68646C72
MINF = 0x6D696E66
STBL = 0x7374626C
STSD = 0x73747364
EDTS = 0x65647473
ELST = 0x656C7374
UDTA = 0x75647461

CONTAINER_BOXES = {MOOV, TRAK, MDIA, MINF, STBL, EDTS, UDTA}

HANDLER_KINDS = {
    "vide": KIND_VIDEO,
    "soun": KIND_AUDIO,
    "text": KIND_SUBTITLE,
    "sbtl": KIND_SUBTITLE,
    "subt": KIND_SUBTITLE,
    "clcp": KIND_SUBTITLE,
}

MAX_BOX_DEPTH = 32


def issue(code: str, message: str, offset: int,
          severity: str = SEVERITY_ERROR,
          track_id: int | None = None) -> Issue:
    return Issue(code=code, message=message, offset=offset,
                 severity=severity, track_id=track_id)


def fourcc(value: int) -> str:
    """Render a 4-byte box type as text, keeping non-ASCII visible."""
    raw = value.to_bytes(4, "big")
    return "".join(chr(b) if 32 <= b < 127 else f"\\x{b:02x}" for b in raw)


def fourcc_bytes(raw: bytes) -> str:
    value = int.from_bytes(raw, "big")
    return fourcc(value)


class Box:
    """A parsed box header; ``body`` returns a fresh bounded reader."""

    __slots__ = ("type", "start", "header_size", "size", "data")

    def __init__(self, type_: int, start: int, header_size: int, size: int,
                 data: bytes):
        self.type = type_
        self.start = start
        self.header_size = header_size
        self.size = size
        self.data = data

    @property
    def body_start(self) -> int:
        return self.start + self.header_size

    @property
    def body(self) -> Reader:
        return Reader(self.data, self.body_start, self.start + self.size,
                      f"box@{self.start}:{self.name}")

    @property
    def name(self) -> str:
        return fourcc(self.type)

    @property
    def end(self) -> int:
        return self.start + self.size

    def __repr__(self) -> str:  # pragma: no cover - debugging aid
        return f"<Box {self.name} at {self.start} size {self.size}>"


def iter_boxes(reader: Reader, media: MediaFile, depth: int = 0,
               path: str = "") -> Iterator[Box]:
    """Yield boxes from ``reader``, recording recoverable errors on ``media``.

    On an unrecoverable length problem the generator stops: the remaining
    bytes of this parent are untrustworthy, but the diagnostic is preserved.
    """
    if depth > MAX_BOX_DEPTH:
        media.add_issue(issue(
            "mp4.depth_exceeded",
            f"box nesting deeper than {MAX_BOX_DEPTH} in {path or 'root'}",
            reader.pos))
        return
    while reader.remaining > 0:
        start = reader.pos
        if reader.remaining < 8:
            media.add_issue(issue(
                "mp4.truncated_header",
                f"{reader.remaining} trailing byte(s) cannot hold a box header",
                start, SEVERITY_WARNING))
            return
        size32 = reader.read_uint32()
        type_ = reader.read_uint32()
        header_size = 8
        if size32 == 1:
            if reader.remaining < 8:
                media.add_issue(issue("mp4.truncated_largesize",
                                      "extended box size field is truncated",
                                      start))
                return
            size = reader.read_uint64()
            header_size = 16
        elif size32 == 0:
            size = reader.limit - start
        else:
            size = size32
        if size < header_size:
            media.add_issue(issue(
                "mp4.invalid_box_size",
                f"box {fourcc(type_)} declares size {size}, smaller than its "
                f"{header_size}-byte header", start))
            return
        try:
            checked_box_end(start, size, reader.limit, "box")
        except BinaryError as exc:
            media.add_issue(issue("mp4.box_overrun", exc.reason, exc.offset))
            return
        body = reader.subreader(size - header_size)
        reader.pos = start + size
        yield Box(type_, start, header_size, size, body.data)


# -- leaf boxes ------------------------------------------------------------

def read_fullbox(reader: Reader) -> tuple[int, int]:
    raw = reader.read_uint32()
    return raw >> 24, raw & 0x00FFFFFF


def parse_mvhd(box: Box) -> tuple[int, int]:
    """Return ``(timescale, duration)``; an all-ones duration becomes 0."""
    body = box.body
    version, _flags = read_fullbox(body)
    if version == 1:
        body.skip(16)
        timescale = body.read_uint32()
        duration = body.read_uint64()
        if duration == 0xFFFFFFFFFFFFFFFF:
            duration = 0
    else:
        body.skip(8)
        timescale = body.read_uint32()
        duration = body.read_uint32()
        if duration == 0xFFFFFFFF:
            duration = 0
    return timescale, duration


def parse_mdhd(box: Box) -> tuple[int, int, bool]:
    """Return ``(timescale, duration, was_unknown_marker)``."""
    body = box.body
    version, _flags = read_fullbox(body)
    if version == 1:
        body.skip(16)
        timescale = body.read_uint32()
        duration = body.read_uint64()
        unknown = duration == 0xFFFFFFFFFFFFFFFF
    else:
        body.skip(8)
        timescale = body.read_uint32()
        duration = body.read_uint32()
        unknown = duration == 0xFFFFFFFF
    return timescale, (0 if unknown else duration), unknown


def parse_tkhd(box: Box) -> tuple[int, int]:
    """Return ``(track_id, duration_in_movie_timescale)``."""
    body = box.body
    version, _flags = read_fullbox(body)
    if version == 1:
        body.skip(16)
        track_id = body.read_uint32()
        body.skip(4)
        duration = body.read_uint64()
        if duration == 0xFFFFFFFFFFFFFFFF:
            duration = 0
    else:
        body.skip(8)
        track_id = body.read_uint32()
        body.skip(4)
        duration = body.read_uint32()
        if duration == 0xFFFFFFFF:
            duration = 0
    return track_id, duration


def parse_hdlr(box: Box) -> str:
    body = box.body
    _version, _flags = read_fullbox(body)
    body.skip(4)  # pre_defined
    return body.read_bytes(4).decode("latin-1", "replace")


def parse_elst(box: Box) -> list[tuple[int, int]]:
    """Return ``(segment_duration, media_time)`` entries, capped at 1024."""
    body = box.body
    version, _flags = read_fullbox(body)
    count = body.read_uint32()
    if count > 1024:
        raise BinaryError(f"elst declares {count} entries (limit 1024)",
                          body.pos - 4)
    entries = []
    for _ in range(count):
        if version == 1:
            seg = body.read_uint64()
            media_time = int.from_bytes(body.read_bytes(8), "big",
                                        signed=True)
        else:
            seg = body.read_uint32()
            media_time = int.from_bytes(body.read_bytes(4), "big",
                                        signed=True)
        body.skip(4)  # media_rate integer + fraction
        entries.append((seg, media_time))
    return entries


def parse_stsd(box: Box, kind: str, issues: list[Issue],
               track_id: int) -> tuple[str, dict[str, Any]]:
    """Return ``(codec, codec_detail)`` from the first sample entry."""
    body = box.body
    _version, _flags = read_fullbox(body)
    count = body.read_uint32()
    if count == 0:
        issues.append(issue("mp4.stsd_empty",
                            "stsd declares zero sample entries",
                            box.start, track_id=track_id))
        return "unknown", {}
    entry_start = body.pos
    entry_size = body.read_uint32()
    entry_type = body.read_bytes(4).decode("latin-1", "replace")
    if entry_size < 8 or entry_start + entry_size > body.limit:
        raise BinaryError(
            f"sample entry {entry_type!r} size {entry_size} exceeds stsd "
            f"bounds", entry_start)
    detail: dict[str, Any] = {"sample_entry": entry_type}
    entry = body.subreader(entry_size - 8)
    if kind == KIND_VIDEO:
        entry.skip(6 + 2 + 16)  # reserved, data_ref_index, predef+reserved
        detail["width"] = entry.read_uint16()
        detail["height"] = entry.read_uint16()
    elif kind == KIND_AUDIO:
        entry.skip(6 + 2 + 8)  # reserved, data_ref_index, version+reserved
        detail["channels"] = entry.read_uint16()
        detail["sample_size"] = entry.read_uint16()
        entry.skip(4)  # pre_defined + reserved
        detail["sample_rate"] = entry.read_uint32() >> 16
    return entry_type, detail
