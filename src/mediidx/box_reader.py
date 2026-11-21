"""Bounded ISO BMFF box reading.

The reader never trusts a declared size: every header and payload is checked
against the enclosing parent range before it is exposed.  Boxes keep their
absolute file offsets so later diagnostics can name both the box type and the
byte position that failed.
"""

from __future__ import annotations

import struct
from dataclasses import dataclass, field

from .errors import StructureError
from .cursor import Cursor

MAX_FILE_BYTES = 256 * 1024 * 1024
MAX_BOX_DEPTH = 8
MAX_BOX_COUNT = 100_000

_HEADER = struct.Struct(">I4s")
_U64 = struct.Struct(">Q")
# Containers that this tool descends into.  Payloads of other boxes (notably
# mdat) are never parsed as children, which also keeps the reader cheap.
CONTAINER_TYPES = frozenset(
    {b"moov", b"trak", b"mdia", b"minf", b"stbl", b"edts", b"dinf"}
)


@dataclass(frozen=True)
class Box:
    type: bytes
    start: int
    size: int
    header_size: int
    children: tuple["Box", ...] = field(default_factory=tuple)

    @property
    def payload_start(self) -> int:
        return self.start + self.header_size

    @property
    def end(self) -> int:
        return self.start + self.size

    @property
    def payload_size(self) -> int:
        return self.end - self.payload_start

    @property
    def label(self) -> str:
        return self.type.decode("latin-1", "replace")

    def payload(self, view: memoryview) -> memoryview:
        return view[self.payload_start : self.end]

    def child(self, box_type: bytes) -> "Box | None":
        for item in self.children:
            if item.type == box_type:
                return item
        return None

    def children_of(self, box_type: bytes) -> list["Box"]:
        return [item for item in self.children if item.type == box_type]

    def walk(self):
        yield self
        for child in self.children:
            yield from child.walk()


def _require_room(offset: int, need: int, end: int, box_type: bytes) -> None:
    if need < 0 or offset + need > end:
        raise StructureError(
            f"truncated box header: need {need} bytes, only {end - offset} remain",
            box_type=box_type,
            offset=offset,
        )


class BoxReader:
    """Parse a bounded tree of boxes from a flat byte view."""

    def __init__(
        self,
        view: memoryview,
        *,
        max_depth: int = MAX_BOX_DEPTH,
        max_boxes: int = MAX_BOX_COUNT,
    ) -> None:
        self.view = view
        self.max_depth = max_depth
        self.max_boxes = max_boxes
        self.count = 0
        self.broken_sidx = False

    # -- public API -----------------------------------------------------
    def parse_top_level(self, *, tolerate_broken_sidx: bool = False) -> list[Box]:
        boxes, _broken = self._parse_list(
            0, len(self.view), 0, b"root", tolerate_broken_sidx=tolerate_broken_sidx
        )
        return boxes

    def parse_children(self, box: Box, *, depth: int = 1) -> list[Box]:
        children, _broken = self._parse_list(
            box.payload_start, box.end, depth, box.type
        )
        return children

    # -- internals ------------------------------------------------------
    def _parse_list(
        self,
        start: int,
        end: int,
        depth: int,
        parent: bytes,
        *,
        tolerate_broken_sidx: bool = False,
    ) -> tuple[list[Box], bool]:
        boxes: list[Box] = []
        offset = start
        while offset < end:
            leftover = end - offset
            if leftover < 8:
                # Trailing padding smaller than a header is only acceptable as
                # zero bytes; anything else means the box stream is damaged.
                if any(self.view[offset:end]):
                    raise StructureError(
                        f"{leftover} trailing non-zero bytes are too short for a box header",
                        box_type=parent,
                        offset=offset,
                    )
                break
            size, box_type = _HEADER.unpack_from(self.view, offset)
            header_size = 8
            if size == 1:
                _require_room(offset + 8, 8, end, box_type)
                size = _U64.unpack_from(self.view, offset + 8)[0]
                header_size = 16
            elif size == 0:
                size = end - offset
            if box_type == b"uuid":
                header_size += 16
                _require_room(offset, header_size, end, box_type)
            if size < header_size:
                raise StructureError(
                    f"declared size {size} is smaller than its {header_size}-byte header",
                    box_type=box_type,
                    offset=offset,
                )
            if size > end - offset:
                broken_type = box_type
                if tolerate_broken_sidx and broken_type == b"sidx" and parent == b"root":
                    # A truncated trailing index must not hide a usable sample
                    # table: report it as a warning instead of a parse failure.
                    self.broken_sidx = True
                    return boxes, True
                raise StructureError(
                    f"declared size {size} exceeds the {end - offset} bytes "
                    f"available in parent {parent.decode('latin-1', 'replace')}",
                    box_type=box_type,
                    offset=offset,
                )
            self.count += 1
            if self.count > self.max_boxes:
                raise StructureError(
                    f"box count exceeds limit {self.max_boxes}",
                    box_type=box_type,
                    offset=offset,
                )
            children: tuple[Box, ...] = ()
            if box_type in CONTAINER_TYPES:
                if depth >= self.max_depth:
                    raise StructureError(
                        f"box nesting exceeds depth limit {self.max_depth}",
                        box_type=box_type,
                        offset=offset,
                    )
                kids, _ = self._parse_list(
                    offset + header_size, offset + size, depth + 1, box_type
                )
                children = tuple(kids)
            boxes.append(
                Box(
                    type=box_type,
                    start=offset,
                    size=size,
                    header_size=header_size,
                    children=children,
                )
            )
            offset += size
        return boxes, False


def load_view(data: bytes) -> memoryview:
    return memoryview(data)


def read_file_bytes(path: str, *, max_bytes: int = MAX_FILE_BYTES):
    """Read a local file with a hard size cap; I/O problems become UsageError."""

    from .errors import UsageError
    import os

    try:
        stat = os.stat(path)
    except OSError as exc:
        raise UsageError(f"cannot stat {path}: {exc}") from exc
    if not os.path.isfile(path):
        raise UsageError(f"{path} is not a regular file")
    if stat.st_size > max_bytes:
        raise UsageError(
            f"{path} is {stat.st_size} bytes, above the {max_bytes}-byte read limit"
        )
    try:
        with open(path, "rb") as handle:
            data = handle.read(max_bytes + 1)
    except OSError as exc:
        raise UsageError(f"cannot read {path}: {exc}") from exc
    if len(data) > max_bytes:
        raise UsageError(f"{path} grew beyond the {max_bytes}-byte read limit")
    return data


def find_first(boxes: list[Box], box_type: bytes) -> Box | None:
    for box in boxes:
        if box.type == box_type:
            return box
    return None


def find_all(boxes: list[Box], box_type: bytes) -> list[Box]:
    return [box for box in boxes if box.type == box_type]
