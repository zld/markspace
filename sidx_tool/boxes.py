"""Bounded BMFF box parser and serializer.

Supports nested containers, 32/64-bit sizes (``largesize``), unknown-box
skipping and end-of-file boundary checks.  Every error carries the box path
and absolute file offset where it was detected.
"""

from __future__ import annotations

import struct
from dataclasses import dataclass, field
from pathlib import Path
from typing import BinaryIO

from . import MAX_BOX_DEPTH, MAX_BOXES, MAX_BOX_SIZE
from .errors import FormatError, LimitError

# Boxes whose payload is itself a sequence of child boxes.
CONTAINER_TYPES = frozenset(
    {"moov", "trak", "mdia", "minf", "dinf", "stbl", "edts", "mvex", "moof", "traf"}
)
# FullBox container: 4-byte version/flags prefix before children.
FULL_CONTAINER_TYPES = frozenset({"meta"})
HEADER_SIZE = 8
LARGE_HEADER_SIZE = 16
FULLBOX_PREFIX = 4


@dataclass
class Box:
    """One parsed BMFF box.

    ``start`` / ``header_end`` / ``end`` are absolute file offsets; children
    of a container share the same backing file and stay offset-addressable.
    """

    type: str
    start: int
    header_end: int
    end: int
    children: list["Box"] = field(default_factory=list)
    path: str = ""

    @property
    def size(self) -> int:
        return self.end - self.start

    def find(self, box_type: str) -> "Box | None":
        for child in self.children:
            if child.type == box_type:
                return child
        return None

    def find_path(self, *types: str) -> "Box | None":
        node: Box | None = self
        for box_type in types:
            node = node.find(box_type) if node is not None else None
        return node

    def find_all(self, box_type: str) -> list["Box"]:
        return [c for c in self.children if c.type == box_type]


def _read_exact(stream: BinaryIO, count: int, what: str) -> bytes:
    data = stream.read(count)
    if len(data) != count:
        raise FormatError(f"truncated box header while reading {what} at offset {stream.tell() - len(data)}")
    return data


def parse_file(path: str | Path) -> list[Box]:
    """Parse every top-level box of *path* with full boundary checking."""
    path = Path(path)
    try:
        file_size = path.stat().st_size
        with path.open("rb") as stream:
            return _parse_children(stream, 0, file_size, depth=0, path_prefix="")
    except OSError as exc:
        from .errors import FileError

        raise FileError(f"cannot read {path}: {exc}") from exc


def _parse_children(
    stream: BinaryIO,
    limit_start: int,
    limit_end: int,
    depth: int,
    path_prefix: str,
) -> list[Box]:
    boxes: list[Box] = []
    box_count = 0
    while True:
        start = stream.tell()
        if start > limit_end:
            raise FormatError(f"box at offset {start} overruns parent end {limit_end} ({path_prefix or '/'})")
        if start == limit_end:
            return boxes
        if len(boxes) >= MAX_BOXES:
            raise LimitError(f"box count exceeds safety limit {MAX_BOXES} at offset {start}")
        header = _read_exact(stream, HEADER_SIZE, "size/type")
        size32, box_type_bytes = struct.unpack(">I4s", header)
        try:
            box_type = box_type_bytes.decode("ascii")
        except UnicodeDecodeError:
            raise FormatError(f"non-ASCII box type {box_type_bytes!r} at offset {start}")
        if not all(0x20 <= c <= 0x7E for c in box_type_bytes):
            raise FormatError(f"invalid box type {box_type!r} at offset {start}")
        header_end = start + HEADER_SIZE
        if size32 == 1:  # largesize
            large = struct.unpack(">Q", _read_exact(stream, 8, "largesize"))[0]
            size = large
            header_end = start + LARGE_HEADER_SIZE
        elif size32 == 0:  # box extends to end of file
            size = limit_end - start
        else:
            size = size32
        if size < 0:
            raise FormatError(f"negative box size for {box_type} at offset {start}")
        if size < header_end - start:
            raise FormatError(f"box {box_type} size {size} smaller than its header at offset {start}")
        end = start + size
        if end > limit_end:
            raise FormatError(
                f"box {box_type} at offset {start} ends at {end}, beyond boundary {limit_end}"
            )
        if size > MAX_BOX_SIZE:
            raise LimitError(f"box {box_type} at offset {start} exceeds maximum size {MAX_BOX_SIZE}")
        current_path = f"{path_prefix}/{box_type}" if path_prefix else box_type
        box = Box(box_type, start, header_end, end, path=current_path)
        if box_type in CONTAINER_TYPES or box_type in FULL_CONTAINER_TYPES:
            if depth + 1 > MAX_BOX_DEPTH:
                raise LimitError(f"box nesting exceeds depth {MAX_BOX_DEPTH} at {current_path}")
            child_start = header_end
            if box_type in FULL_CONTAINER_TYPES:
                child_start += FULLBOX_PREFIX
            stream.seek(child_start)
            box.children = _parse_children(stream, child_start, end, depth + 1, current_path)
        stream.seek(end)
        boxes.append(box)
        box_count += 1


def read_payload(stream: BinaryIO, box: Box) -> bytes:
    """Return the full payload (header excluded) of *box*."""
    stream.seek(box.header_end)
    length = box.end - box.header_end
    data = stream.read(length)
    if len(data) != length:
        raise FormatError(f"truncated payload for {box.path} at offset {box.start}")
    return data


def read_fullbox_payload(stream: BinaryIO, box: Box) -> tuple[int, int, bytes]:
    """Split a FullBox payload into ``(version, flags, remaining_bytes)``."""
    payload = read_payload(stream, box)
    if len(payload) < FULLBOX_PREFIX:
        raise FormatError(f"FullBox {box.path} payload shorter than 4 bytes")
    version = payload[0]
    flags = int.from_bytes(payload[1:4], "big")
    return version, flags, payload[FULLBOX_PREFIX:]


def make_box(box_type: str, payload: bytes, fullbox: bool = False, version: int = 0, flags: int = 0) -> bytes:
    """Serialize one leaf box (optionally a FullBox) with a 32-bit size."""
    if fullbox:
        payload = bytes([version]) + flags.to_bytes(3, "big") + payload
    total = HEADER_SIZE + len(payload)
    if total > 0xFFFFFFFF:
        raise FormatError(f"box {box_type} too large for 32-bit size ({total} bytes)")
    return struct.pack(">I4s", total, box_type.encode("ascii")) + payload
