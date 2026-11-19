"""ISO BMFF box parsing with bounded, depth-limited traversal.

Handles 32-bit sizes, 64-bit extended sizes (size32 == 1) and
zero-size boxes that extend to the end of their parent. Any box that
would exceed its parent's bounds, or whose declared size is smaller
than its own header (the on-disk equivalent of a negative length), is
rejected with the file offset and box type attached.
"""
from __future__ import annotations

import struct
from dataclasses import dataclass, field

MAX_FILE_SIZE = 2 * 1024**3  # 2 GiB hard input limit
MAX_DEPTH = 32               # box nesting limit

CONTAINER_TYPES = frozenset({
    "moov", "trak", "mdia", "minf", "stbl", "moof", "traf",
    "edts", "dinf", "mvex", "udta",
})


class StructuralError(Exception):
    """Input file is structurally malformed (CLI exit code 2)."""


class BoxError(StructuralError):
    """Structural error while parsing boxes."""

    def __init__(self, message, offset=None, box_type=None):
        self.offset = offset
        self.box_type = box_type
        text = message
        if box_type is not None:
            text = f"{text} (box={box_type!r})"
        if offset is not None:
            text = f"{text} at offset {offset}"
        super().__init__(text)


@dataclass
class Box:
    type: str
    offset: int        # absolute offset of the box header
    size: int          # total size including header
    header_size: int
    depth: int
    children: list = field(default_factory=list)

    @property
    def data_offset(self):
        return self.offset + self.header_size

    @property
    def end(self):
        return self.offset + self.size

    @property
    def payload_size(self):
        return self.size - self.header_size


def parse_boxes(data, start=0, end=None, depth=0):
    """Parse the box sequence in data[start:end]; returns a list of Box."""
    if end is None:
        end = len(data)
    if depth > MAX_DEPTH:
        raise BoxError(f"nesting depth exceeds {MAX_DEPTH}", offset=start)
    boxes = []
    pos = start
    while pos < end:
        if end - pos < 8:
            raise BoxError("trailing bytes too small for a box header", offset=pos)
        size32, raw_type = struct.unpack_from(">I4s", data, pos)
        try:
            box_type = raw_type.decode("ascii")
        except UnicodeDecodeError:
            raise BoxError("non-ascii box type", offset=pos) from None
        header = 8
        if size32 == 1:
            if end - pos < 16:
                raise BoxError("truncated extended-size header",
                               offset=pos, box_type=box_type)
            (size,) = struct.unpack_from(">Q", data, pos + 8)
            header = 16
        elif size32 == 0:
            size = end - pos  # zero-size box extends to end of parent
        else:
            size = size32
        if size < header:
            raise BoxError(f"box size {size} smaller than header {header}",
                           offset=pos, box_type=box_type)
        if pos + size > end:
            raise BoxError(
                f"box size {size} exceeds parent bounds (parent ends at {end})",
                offset=pos, box_type=box_type)
        box = Box(type=box_type, offset=pos, size=size,
                  header_size=header, depth=depth)
        if box_type in CONTAINER_TYPES and box.payload_size > 0:
            box.children = parse_boxes(data, box.data_offset, box.end, depth + 1)
        boxes.append(box)
        pos += size
    return boxes


def find_child(box, box_type):
    """First direct child of the given type, or None."""
    return next((c for c in box.children if c.type == box_type), None)
