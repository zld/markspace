"""Generic EBML element iteration with hard boundaries.

This module knows about EBML framing only.  Matroska semantics live in
:mod:`mediacheck.matroska`.  Unknown-size masters are bounded only when the
enclosing boundary is known (a Segment by end-of-file, a Cluster by the next
segment-level sibling); otherwise iteration stops rather than scanning blindly.
"""

from __future__ import annotations

import struct
from typing import Iterator

from .binary import BinaryError, Reader, MAX_VINT_LEN
from .model import Issue, MediaFile, SEVERITY_ERROR

EBML_HEADER = 0x1A45DFA3
SEGMENT = 0x18538067
CLUSTER = 0x1F43B675

MAX_ELEMENTS_PER_LEVEL = 100_000
MAX_MASTER_DEPTH = 16

# Segment-level siblings used as stop markers when a Cluster has no size.
SEGMENT_LEVEL_IDS = {
    0x1F43B675,  # Cluster
    0x1C53BB6B,  # Cues
    0x114D9B74,  # SeekHead
    0x1254C367,  # Tags
    0x1043A770,  # Chapters
    0x1941A469,  # Attachments
    0x1654AE6B,  # Tracks
    0x1549A966,  # Info
}


def issue(code: str, message: str, offset: int,
          severity: str = SEVERITY_ERROR) -> Issue:
    return Issue(code=code, message=message, offset=offset, severity=severity)


class Element:
    """An EBML element header; ``body`` returns a fresh bounded reader."""

    __slots__ = ("id", "start", "header_size", "size", "unknown", "data")

    def __init__(self, id_: int, start: int, header_size: int, size: int,
                 unknown: bool, data: bytes):
        self.id = id_
        self.start = start
        self.header_size = header_size
        self.size = size
        self.unknown = unknown
        self.data = data

    @property
    def data_start(self) -> int:
        return self.start + self.header_size

    @property
    def end(self) -> int:
        return self.data_start + self.size

    @property
    def body(self) -> Reader:
        return Reader(self.data, self.data_start, self.end,
                      f"ebml@0x{self.start:x}")

    def __repr__(self) -> str:  # pragma: no cover - debugging aid
        return f"<Element 0x{self.id:x} at {self.start} size {self.size}>"


def read_ebml_id(reader: Reader) -> tuple[int, int]:
    """Read an EBML element ID, returning ``(value, length)``.

    The marker bit is kept in the value, because Matroska IDs are defined
    including it (e.g. ``0x1A45DFA3`` for Segment).
    """
    start = reader.pos
    first = reader.read_uint8()
    if first == 0:
        raise BinaryError("EBML ID has zero leading byte", start)
    length = 1
    mask = 0x80
    while length <= 4 and not (first & mask):
        mask >>= 1
        length += 1
    if length > 4:
        raise BinaryError("EBML ID longer than 4 bytes", start)
    value = first
    for _ in range(length - 1):
        value = (value << 8) | reader.read_uint8()
    return value, length


def read_ebml_size(reader: Reader) -> tuple[int | None, int]:
    """Read an EBML data size, returning ``(value_or_None, length)``.

    ``None`` means the all-ones "unknown size" marker.  The marker bit is
    stripped from the returned value.
    """
    start = reader.pos
    first = reader.read_uint8()
    if first == 0:
        raise BinaryError("EBML size has zero leading byte", start)
    length = 1
    mask = 0x80
    while length <= MAX_VINT_LEN and not (first & mask):
        mask >>= 1
        length += 1
    if length > MAX_VINT_LEN:
        raise BinaryError("EBML size longer than 8 bytes", start)
    raw = first & (mask - 1)
    for _ in range(length - 1):
        raw = (raw << 8) | reader.read_uint8()
    all_ones = (1 << (7 * length)) - 1
    if raw == all_ones:
        return None, length
    return raw, length


def encode_ebml_size(value: int) -> bytes:
    """Encode ``value`` as a minimal-width EBML size (used by the writer)."""
    if value < 0:
        raise ValueError("EBML size must be non-negative")
    for length in range(1, MAX_VINT_LEN + 1):
        if value < (1 << (7 * length)) - 1:
            return ((1 << (7 * length)) | value).to_bytes(length, "big")
    raise ValueError(f"EBML size {value} does not fit in 8 bytes")


def encode_ebml_id(value: int) -> bytes:
    """Encode an EBML element ID, validating its marker width."""
    for length in range(1, 5):
        if value < (1 << (8 * length)):
            raw = value.to_bytes(length, "big")
            if raw[0] == 0:
                break
            marker = 0x80
            for candidate in range(1, 5):
                if raw[0] & marker:
                    if candidate == length:
                        return raw
                    break
                marker >>= 1
    raise ValueError(f"invalid EBML ID 0x{value:x}")


def read_vint_value(reader: Reader) -> int:
    """Read a marker-stripped EBML vint (used for block track numbers)."""
    value, _length = read_ebml_size(reader)
    if value is None:
        raise BinaryError("block track number uses the unknown-size marker",
                          reader.pos - 1)
    return value


def read_uint(reader: Reader) -> int:
    return reader.read_uint(reader.remaining)


def read_float(reader: Reader) -> float:
    size = reader.remaining
    if size == 4:
        return struct.unpack(">f", reader.read_bytes(4))[0]
    if size == 8:
        return struct.unpack(">d", reader.read_bytes(8))[0]
    if size == 0:
        return 0.0
    raise BinaryError(f"unsupported float width {size}", reader.pos)


def read_text(reader: Reader) -> str:
    return (reader.read_bytes(reader.remaining)
            .decode("utf-8", "replace").rstrip("\x00"))


def scan_unknown_end(reader: Reader, stop_ids: set[int]) -> int:
    """Find the end of an unknown-size master by scanning sibling headers."""
    scan = Reader(reader.data, reader.pos, reader.limit, reader.name)
    count = 0
    while scan.remaining > 0 and count < MAX_ELEMENTS_PER_LEVEL:
        count += 1
        start = scan.pos
        try:
            id_, _ = read_ebml_id(scan)
            if id_ in stop_ids:
                return start
            size, _ = read_ebml_size(scan)
        except BinaryError:
            return start
        if size is None or size > scan.remaining:
            return start
        scan.skip(size)
    return scan.pos


def iter_elements(reader: Reader, media: MediaFile, depth: int = 0,
                  stop_ids: set[int] | None = None) -> Iterator[Element]:
    """Yield elements until ``reader`` is exhausted or a stop-ID is reached."""
    if depth > MAX_MASTER_DEPTH:
        media.add_issue(issue(
            "mkv.depth_exceeded",
            f"EBML nesting deeper than {MAX_MASTER_DEPTH}", reader.pos))
        return
    count = 0
    while reader.remaining > 0:
        count += 1
        if count > MAX_ELEMENTS_PER_LEVEL:
            media.add_issue(issue(
                "mkv.element_limit",
                f"more than {MAX_ELEMENTS_PER_LEVEL} elements in one level",
                reader.pos))
            return
        start = reader.pos
        try:
            id_, id_len = read_ebml_id(reader)
            size, size_len = read_ebml_size(reader)
        except BinaryError as exc:
            media.add_issue(issue("mkv.truncated_element_header",
                                  exc.reason, exc.offset))
            return
        if stop_ids is not None and id_ in stop_ids and depth > 0:
            reader.pos = start  # leave the sibling for the caller
            return
        header_size = id_len + size_len
        if size is None:
            end = _unknown_size_end(id_, start, reader, media)
            if end is None:
                return
        else:
            end = reader.pos + size
            if end > reader.limit:
                media.add_issue(issue(
                    "mkv.element_overrun",
                    f"element 0x{id_:x} declares {size} byte(s) but only "
                    f"{reader.limit - reader.pos} remain", start))
                return
        yield Element(id_, start, header_size, end - reader.pos, size is None,
                      reader.data)
        reader.pos = end


def _unknown_size_end(id_: int, start: int, reader: Reader,
                      media: MediaFile) -> int | None:
    """Resolve the end offset of an unknown-size element, or ``None`` to stop."""
    if id_ == SEGMENT:
        media.add_issue(issue(
            "mkv.unknown_size_segment",
            "Segment has unknown size; bounded by end of file",
            start, "info"))
        return reader.limit
    if id_ == CLUSTER:
        end = scan_unknown_end(
            Reader(reader.data, reader.pos, reader.limit, reader.name),
            SEGMENT_LEVEL_IDS)
        media.add_issue(issue(
            "mkv.unknown_size_cluster",
            f"Cluster at {start} has unknown size; boundary inferred at {end}",
            start, "info"))
        return end
    media.add_issue(issue(
        "mkv.unknown_size",
        f"element 0x{id_:x} has unknown size outside a known boundary; "
        f"skipping the rest of this level", start))
    return None
