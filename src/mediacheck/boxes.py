"""Recursive ISO BMFF / QuickTime box parsing.

Only headers are read while walking the tree; leaf payloads are fetched on
demand by the sniffer.  Two limits are enforced here:

* nesting deeper than :data:`MAX_BOX_DEPTH` is a structural error;
* a box that claims to extend past its parent/file is reported as a
  truncation, either as an exception (strict) or a warning (tolerant).
"""

from __future__ import annotations

import struct
from dataclasses import dataclass, field
from typing import BinaryIO, Iterable

from .util import MAX_BOX_DEPTH

MAX_BOXES = 100_000
_HEADER = struct.Struct(">I4s")
_LARGE = struct.Struct(">Q")


@dataclass(slots=True)
class Box:
    type: str
    offset: int
    size: int
    header_size: int
    payload_offset: int
    payload_size: int
    children: list["Box"] = field(default_factory=list)

    def find(self, type_name: str) -> "Box | None":
        for child in self.children:
            if child.type == type_name:
                return child
        return None

    def find_all(self, type_name: str) -> list["Box"]:
        return [c for c in self.children if c.type == type_name]


@dataclass(slots=True)
class BoxTree:
    boxes: list[Box]
    truncated: bool
    warnings: list[str]
    file_size: int

    def find(self, type_name: str) -> Box | None:
        for box in self.boxes:
            if box.type == type_name:
                return box
        return None

    def walk(self) -> Iterable[Box]:
        def _walk(items: list[Box]) -> Iterable[Box]:
            for item in items:
                yield item
                yield from _walk(item.children)

        return _walk(self.boxes)


# Boxes whose payload is itself a sequence of boxes.  Full-box containers
# such as ``stsd`` carry a version/count preamble and are parsed explicitly
# by the sniffer instead.
CONTAINER_TYPES = frozenset(
    {
        "moov",
        "trak",
        "mdia",
        "minf",
        "stbl",
        "edts",
        "mvex",
        "moof",
        "traf",
        "mfra",
        "udta",
        "tref",
        "ipro",
        "sinf",
    }
)


def _read_exact(handle: BinaryIO, count: int) -> bytes:
    data = handle.read(count)
    if len(data) != count:
        raise EOFError(f"wanted {count} bytes, got {len(data)}")
    return data


@dataclass(slots=True)
class _Context:
    handle: BinaryIO
    file_size: int
    strict: bool
    max_depth: int
    truncated: bool = False
    warnings: list[str] = field(default_factory=list)
    box_count: int = 0

    def overflow(self, message: str, offset: int) -> None:
        if self.strict:
            from .model import MediaError

            raise MediaError(message, offset)
        self.truncated = True
        self.warnings.append(f"{message} (offset {offset})")

    def structural(self, message: str, offset: int) -> None:
        from .model import MediaError

        raise MediaError(message, offset)


def parse_box_tree(
    handle: BinaryIO,
    file_size: int,
    *,
    strict: bool = False,
    max_depth: int = MAX_BOX_DEPTH,
) -> BoxTree:
    """Parse all top-level boxes, recursing into container boxes."""
    ctx = _Context(handle=handle, file_size=file_size, strict=strict, max_depth=max_depth)
    boxes = _parse_range(ctx, 0, file_size, depth=0)
    return BoxTree(
        boxes=boxes,
        truncated=ctx.truncated,
        warnings=ctx.warnings,
        file_size=file_size,
    )


def _parse_range(ctx: _Context, start: int, end: int, depth: int) -> list[Box]:
    if depth > ctx.max_depth:
        ctx.structural(
            f"box nesting exceeds depth limit {ctx.max_depth}", start
        )
    boxes: list[Box] = []
    position = start
    handle = ctx.handle
    while position < end:
        remaining = end - position
        if remaining < 8:
            ctx.overflow(
                f"trailing {remaining} byte(s) cannot form a box header", position
            )
            break
        handle.seek(position)
        try:
            raw_size, raw_type = _HEADER.unpack(_read_exact(handle, 8))
        except EOFError:
            ctx.overflow("unexpected end of file while reading box header", position)
            break
        header_size = 8
        size = raw_size
        if size == 1:
            if remaining < 16:
                ctx.overflow("truncated 64-bit box length", position)
                break
            try:
                (large,) = _LARGE.unpack(_read_exact(handle, 8))
            except EOFError:
                ctx.overflow("truncated 64-bit box length", position)
                break
            size = large
            header_size = 16
        elif size == 0:
            size = end - position

        type_name = raw_type.decode("latin-1")
        if type_name == "uuid":
            header_size += 16
            if remaining < header_size:
                ctx.overflow("truncated uuid box header", position)
                break

        if size < header_size:
            ctx.structural(
                f"box {type_name!r} declares invalid size {size}", position
            )

        box_end = position + size
        if box_end > end:
            # Locatable truncation: report where the incomplete box begins.
            ctx.overflow(
                f"box {type_name!r} declares {size} bytes but only "
                f"{end - position} remain",
                position,
            )
            break

        ctx.box_count += 1
        if ctx.box_count > MAX_BOXES:
            ctx.structural(f"box count exceeds limit {MAX_BOXES}", position)

        box = Box(
            type=type_name,
            offset=position,
            size=size,
            header_size=header_size,
            payload_offset=position + header_size,
            payload_size=size - header_size,
        )
        if type_name in CONTAINER_TYPES and box.payload_size > 0:
            box.children = _parse_range(
                ctx, box.payload_offset, box_end, depth + 1
            )
        boxes.append(box)
        if ctx.truncated and not ctx.strict:
            break
        position = box_end
    return boxes


def read_payload(handle: BinaryIO, box: Box, limit: int = 1 << 20) -> bytes:
    """Read (at most ``limit`` bytes of) a leaf box payload."""
    count = min(box.payload_size, limit)
    handle.seek(box.payload_offset)
    try:
        return _read_exact(handle, count)
    except EOFError as exc:
        from .model import MediaError

        raise MediaError("unexpected end of file while reading payload", box.payload_offset) from exc
