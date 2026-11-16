"""Bounded ISO BMFF box tree reader.

The reader works on a fully buffered byte string: callers enforce a maximum
file size before reading, so a hostile file cannot exhaust memory. Every
parse step is range checked against the enclosing box payload.
"""

from __future__ import annotations

import struct
from dataclasses import dataclass

from .model import (
    DEFAULT_MAX_BOXES,
    DEFAULT_MAX_FILE_BYTES,
    DEFAULT_MAX_NESTING,
    StructureError,
    UsageError,
)

# Boxes whose payload is itself a sequence of child boxes.
CONTAINER_TYPES = frozenset(
    {
        b"moov",
        b"trak",
        b"mdia",
        b"minf",
        b"dinf",
        b"stbl",
        b"mvex",
        b"moof",
        b"traf",
        b"mfra",
        b"edts",
    }
)


@dataclass(frozen=True)
class Box:
    type: bytes
    header_size: int
    payload: bytes
    start: int  # absolute offset of the box header in the file
    children: tuple["Box", ...] = ()

    @property
    def size(self) -> int:
        return self.header_size + len(self.payload)

    @property
    def payload_end(self) -> int:
        return self.start + self.size

    def find(self, box_type: bytes) -> "Box | None":
        for child in self.children:
            if child.type == box_type:
                return child
        return None

    def find_path(self, *path: bytes) -> "Box | None":
        node: Box | None = self
        for box_type in path:
            if node is None:
                return None
            node = node.find(box_type)
        return node

    def find_all(self, box_type: bytes) -> tuple["Box", ...]:
        return tuple(c for c in self.children if c.type == box_type)


def _parse_children(
    data: bytes, base: int, limit: int, depth: int, counters: dict
) -> tuple[Box, ...]:
    children: list[Box] = []
    pos = 0
    while pos < limit:
        if limit - pos < 8:
            raise StructureError("truncated box header")
        header_size = 8
        size32 = struct.unpack_from(">I", data, base + pos)[0]
        box_type = data[base + pos + 4 : base + pos + 8]
        if size32 == 1:
            if limit - pos < 16:
                raise StructureError("truncated 64-bit box header")
            size = struct.unpack_from(">Q", data, base + pos + 8)[0]
            header_size = 16
        else:
            size = size32
        if size == 0:
            # Box extends to end-of-file; only legal for a single top-level
            # box, never inside a bounded container.
            raise StructureError("illegal zero-length box inside container")
        if size < header_size:
            raise StructureError(f"box {box_type!r} smaller than its header")
        if pos + size > limit:
            raise StructureError(f"box {box_type!r} exceeds enclosing range")
        payload_start = pos + header_size
        payload = data[base + payload_start : base + pos + size]
        counters["boxes"] += 1
        if counters["boxes"] > counters["max_boxes"]:
            raise StructureError("box count exceeds configured limit")
        sub_children: tuple[Box, ...] = ()
        if box_type in CONTAINER_TYPES:
            if depth + 1 > counters["max_depth"]:
                raise StructureError("box nesting depth exceeds configured limit")
            sub_children = _parse_children(
                data, base + payload_start, len(payload), depth + 1, counters
            )
        children.append(
            Box(
                type=box_type,
                header_size=header_size,
                payload=payload,
                start=base + pos,
                children=sub_children,
            )
        )
        pos += size
    return tuple(children)


def parse_bytes(
    data: bytes,
    *,
    max_file_bytes: int = DEFAULT_MAX_FILE_BYTES,
    max_nesting: int = DEFAULT_MAX_NESTING,
    max_boxes: int = DEFAULT_MAX_BOXES,
) -> tuple[Box, ...]:
    if len(data) > max_file_bytes:
        raise UsageError("input file exceeds configured maximum size")
    counters = {"boxes": 0, "max_depth": max_nesting, "max_boxes": max_boxes}
    return _parse_children(data, 0, len(data), 1, counters)


def parse_file(
    path: str,
    *,
    max_file_bytes: int = DEFAULT_MAX_FILE_BYTES,
) -> tuple[Box, ...]:
    try:
        with open(path, "rb") as handle:
            data = handle.read(max_file_bytes + 1)
    except OSError as exc:
        raise UsageError(f"cannot read {path}: {exc}") from exc
    if len(data) > max_file_bytes:
        raise UsageError("input file exceeds configured maximum size")
    return parse_bytes(data, max_file_bytes=max_file_bytes)


def read_fullbox(box: Box) -> tuple[int, int]:
    """Return (version, flags) from a FullBox header."""
    if len(box.payload) < 4:
        raise StructureError(f"box {box.type!r} missing FullBox header")
    version = box.payload[0]
    flags = struct.unpack_from(">I", b"\x00" + box.payload[1:4])[0]
    return version, flags


def iter_boxes(root_boxes: tuple[Box, ...], box_type: bytes) -> tuple[Box, ...]:
    found: list[Box] = []

    def walk(boxes: tuple[Box, ...]) -> None:
        for box in boxes:
            if box.type == box_type:
                found.append(box)
            walk(box.children)

    walk(root_boxes)
    return tuple(found)
