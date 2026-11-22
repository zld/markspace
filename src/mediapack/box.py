"""Bounded MP4 box boundary reading.

All reads are offset-bounded: callers seek to explicit positions and read
exactly the bytes of one box, so the media file is never loaded wholesale.
"""
from __future__ import annotations

import struct
from dataclasses import dataclass
from typing import BinaryIO, Iterator

MAX_FILE_BYTES = 2 * 1024**3  # refuse inputs larger than 2 GiB


class BoxError(Exception):
    """Structural error while reading box boundaries."""


@dataclass(frozen=True)
class Box:
    type: str
    start: int  # absolute offset of the box header
    size: int  # total size including header
    header: int  # header size in bytes (8 or 16)

    @property
    def payload_start(self) -> int:
        return self.start + self.header

    @property
    def end(self) -> int:
        return self.start + self.size


def read_box(fh: BinaryIO, offset: int, limit: int) -> Box:
    """Read one box header at ``offset``; ``limit`` is the containing boundary."""
    if offset + 8 > limit:
        raise BoxError(f"truncated box header at offset {offset}")
    fh.seek(offset)
    raw = fh.read(8)
    if len(raw) != 8:
        raise BoxError(f"short read at offset {offset}")
    size, btype = struct.unpack(">I4s", raw)
    header = 8
    if size == 1:
        fh.seek(offset + 8)
        ext = fh.read(8)
        if len(ext) != 8:
            raise BoxError(f"truncated largesize at offset {offset}")
        (size,) = struct.unpack(">Q", ext)
        header = 16
    elif size == 0:
        size = limit - offset
    if size < header:
        raise BoxError(f"invalid box size {size} at offset {offset}")
    if offset + size > limit:
        raise BoxError(
            f"box {btype!r} at {offset} extends past boundary "
            f"({offset + size} > {limit})"
        )
    return Box(btype.decode("latin-1"), offset, size, header)


def iter_boxes(fh: BinaryIO, start: int, end: int) -> Iterator[Box]:
    offset = start
    while offset < end:
        box = read_box(fh, offset, end)
        yield box
        offset = box.end


def read_payload(fh: BinaryIO, box: Box) -> bytes:
    fh.seek(box.payload_start)
    data = fh.read(box.size - box.header)
    if len(data) != box.size - box.header:
        raise BoxError(f"short read for box {box.type} at {box.start}")
    return data


def find_child(fh: BinaryIO, parent: Box, type_: str) -> Box | None:
    for box in iter_boxes(fh, parent.payload_start, parent.end):
        if box.type == type_:
            return box
    return None
