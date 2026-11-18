"""Incremental ISO BMFF (MP4) box reader with boundary validation.

Only box headers and explicitly requested metadata payloads are read, so
memory use stays flat regardless of media file size. Structural problems
are reported as :class:`BoxError` carrying a category and a file offset.
"""
from __future__ import annotations

import os
import struct
from dataclasses import dataclass, field

HEADER_SIZE = 8
LARGE_HEADER_SIZE = 16
MAX_DEPTH = 8
MAX_BOXES = 20000
MAX_PAYLOAD_READ = 64 * 1024 * 1024  # cap for on-demand metadata payloads

ERR_TRUNCATED = "truncated"
ERR_OUT_OF_BOUNDS = "out_of_bounds"
ERR_INVALID_SIZE = "invalid_size"
ERR_DEPTH_EXCEEDED = "depth_exceeded"
ERR_TOO_MANY_BOXES = "too_many_boxes"
ERR_NOT_MP4 = "not_mp4"
ERR_IO = "io"


class BoxError(Exception):
    """Structural container error located at a file offset."""

    def __init__(self, category: str, offset: int, message: str):
        super().__init__(f"{category} at offset {offset}: {message}")
        self.category = category
        self.offset = offset
        self.message = message

    def to_dict(self) -> dict:
        return {"category": self.category, "offset": self.offset,
                "message": self.message}


@dataclass
class Box:
    """A box header. Children are populated for known container boxes."""

    type: str
    offset: int
    size: int  # total size including header
    header_size: int
    children: list["Box"] = field(default_factory=list)

    @property
    def data_offset(self) -> int:
        return self.offset + self.header_size

    @property
    def data_size(self) -> int:
        return self.size - self.header_size

    @property
    def end(self) -> int:
        return self.offset + self.size

    def first(self, type_: str) -> "Box | None":
        for child in self.children:
            if child.type == type_:
                return child
        return None

    def find(self, type_: str) -> list["Box"]:
        return [c for c in self.children if c.type == type_]


CONTAINER_TYPES = frozenset({
    "moov", "trak", "mdia", "minf", "stbl", "edts", "dinf", "udta",
    "mvex", "moof", "traf", "mfra",
})

# Types we understand (not an allowlist: unknown boxes are skipped, not
# rejected, and counted in the unknown-box summary).
KNOWN_TYPES = CONTAINER_TYPES | frozenset({
    "ftyp", "mvhd", "tkhd", "mdhd", "hdlr", "stsd", "stts", "stsc", "stsz",
    "stz2", "stco", "co64", "stss", "ctts", "vmhd", "smhd", "nmhd", "dref",
    "url ", "mdat", "free", "skip", "elst", "sdtp", "iods", "sidx", "prft",
})


def _read_exact(fh, size: int, offset: int) -> bytes:
    data = fh.read(size)
    if len(data) != size:
        raise BoxError(ERR_TRUNCATED, offset,
                       f"expected {size} bytes, got {len(data)}")
    return data


def read_box_tree(path, max_depth: int = MAX_DEPTH,
                  max_boxes: int = MAX_BOXES) -> list[Box]:
    """Parse the box structure of *path* without loading media payloads."""
    try:
        file_size = os.path.getsize(path)
        with open(path, "rb") as fh:
            tree = _parse_level(fh, 0, file_size, 0, max_depth, max_boxes, [0])
    except BoxError:
        raise
    except OSError as exc:
        raise BoxError(ERR_IO, 0, str(exc)) from exc
    if not tree or tree[0].type != "ftyp":
        raise BoxError(ERR_NOT_MP4, 0,
                       "missing leading ftyp box; not an ISO BMFF file")
    return tree


def _parse_level(fh, start: int, end: int, depth: int, max_depth: int,
                 max_boxes: int, counter: list) -> list[Box]:
    boxes: list[Box] = []
    offset = start
    while offset < end:
        counter[0] += 1
        if counter[0] > max_boxes:
            raise BoxError(ERR_TOO_MANY_BOXES, offset,
                           f"box count exceeds limit {max_boxes}")
        if end - offset < HEADER_SIZE:
            raise BoxError(ERR_TRUNCATED, offset,
                           f"{end - offset} trailing bytes too small for a "
                           "box header")
        fh.seek(offset)
        size32, raw_type = struct.unpack(
            ">I4s", _read_exact(fh, HEADER_SIZE, offset))
        type_ = raw_type.decode("latin-1")
        header_size = HEADER_SIZE
        if size32 == 1:  # 64-bit extended size
            largesize = struct.unpack(
                ">Q", _read_exact(fh, 8, offset + HEADER_SIZE))[0]
            header_size = LARGE_HEADER_SIZE
            size = largesize
            if size < LARGE_HEADER_SIZE:
                raise BoxError(ERR_INVALID_SIZE, offset,
                               f"extended size {size} smaller than header")
        elif size32 == 0:  # box extends to the end of its containing extent
            size = end - offset
        else:
            size = size32
            if size < HEADER_SIZE:
                raise BoxError(ERR_INVALID_SIZE, offset,
                               f"declared size {size} smaller than header")
        box_end = offset + size
        if box_end > end:
            category = ERR_TRUNCATED if depth == 0 else ERR_OUT_OF_BOUNDS
            raise BoxError(category, offset,
                           f"box '{type_}' declares end {box_end} beyond "
                           f"containing extent {end}")
        box = Box(type=type_, offset=offset, size=size,
                  header_size=header_size)
        if type_ in CONTAINER_TYPES:
            if depth + 1 > max_depth:
                raise BoxError(ERR_DEPTH_EXCEEDED, offset,
                               f"nesting deeper than {max_depth} levels")
            box.children = _parse_level(fh, box.data_offset, box_end,
                                        depth + 1, max_depth, max_boxes,
                                        counter)
        boxes.append(box)
        offset = box_end
        if size32 == 0:
            break  # a to-end-of-extent box consumes the rest of the level
    return boxes


def read_payload(fh, box: Box, max_payload: int = MAX_PAYLOAD_READ) -> bytes:
    """Read one box payload on demand, refusing oversized declarations."""
    if box.data_size > max_payload:
        raise BoxError(ERR_INVALID_SIZE, box.offset,
                       f"payload of '{box.type}' is {box.data_size} bytes, "
                       f"over the {max_payload}-byte read cap")
    fh.seek(box.data_offset)
    return _read_exact(fh, box.data_size, box.data_offset)


def walk(tree: list[Box]):
    """Yield every box in the tree, depth first."""
    for box in tree:
        yield box
        yield from walk(box.children)


def unknown_summary(tree: list[Box]) -> dict:
    """Count occurrences of box types we do not interpret."""
    counts: dict[str, int] = {}
    for box in walk(tree):
        if box.type not in KNOWN_TYPES:
            counts[box.type] = counts.get(box.type, 0) + 1
    return dict(sorted(counts.items()))
