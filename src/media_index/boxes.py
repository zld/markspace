"""ISO BMFF box header parsing and bounded byte views.

The parser never slices/copies file contents: it works on :class:`memoryview`
windows and always knows the absolute file offset of every byte, so error
messages can name exact positions. All length arithmetic is checked against
the enclosing view and the hard limits in :mod:`media_index.limits`.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Iterator

from . import limits
from .errors import LimitError, ParseError

# Standard fullbox header: one version byte + three flag bytes.
FULLBOX_HEADER = 4


@dataclass(frozen=True)
class BoxHeader:
    """A parsed box header with absolute offsets into the source file."""

    type: str
    header_start: int
    header_size: int
    size: int  # total box size including header
    end: int  # absolute offset immediately after the box

    @property
    def content_start(self) -> int:
        return self.header_start + self.header_size

    @property
    def payload_size(self) -> int:
        return self.size - self.header_size


def u8(view: memoryview, rel: int, base: int = 0) -> int:
    _need(view, rel, 1, base)
    return view[rel]


def u16(view: memoryview, rel: int, base: int = 0) -> int:
    _need(view, rel, 2, base)
    return int.from_bytes(view[rel : rel + 2], "big")


def u24(view: memoryview, rel: int, base: int = 0) -> int:
    _need(view, rel, 3, base)
    return int.from_bytes(view[rel : rel + 3], "big")


def u32(view: memoryview, rel: int, base: int = 0) -> int:
    _need(view, rel, 4, base)
    return int.from_bytes(view[rel : rel + 4], "big")


def u64(view: memoryview, rel: int, base: int = 0) -> int:
    _need(view, rel, 8, base)
    return int.from_bytes(view[rel : rel + 8], "big")


def i32(view: memoryview, rel: int, base: int = 0) -> int:
    _need(view, rel, 4, base)
    return int.from_bytes(view[rel : rel + 4], "big", signed=True)


def _need(view: memoryview, rel: int, width: int, base: int = 0) -> None:
    if rel < 0 or rel + width > len(view):
        raise ParseError(
            f"truncated field: need {width} byte(s) at relative {rel}, "
            f"only {max(0, len(view) - rel)} available",
            base + rel,
        )


def fourcc(raw: bytes) -> str:
    return raw.decode("latin-1")


def read_box_header(view: memoryview, base: int, rel: int) -> BoxHeader:
    """Parse one box header at ``view[rel]`` (absolute offset ``base+rel``).

    Supports 32-bit sizes, 64-bit ``largesize`` (size==1) and the
    end-of-file box convention (size==0).
    """

    start = base + rel
    _need(view, rel, 8)
    size32 = u32(view, rel)
    box_type = fourcc(bytes(view[rel + 4 : rel + 8]))
    if size32 == 1:
        _need(view, rel + 8, 8)
        size = u64(view, rel + 8)
        header_size = 16
        if size < 16:
            raise ParseError(
                f"box '{box_type}' largesize {size} smaller than 16-byte header",
                start,
            )
    elif size32 == 0:
        # Box extends to the end of the enclosing view; this convention is
        # only legal for the final box in that view.
        if rel + 8 != len(view):
            raise ParseError(
                f"box '{box_type}' at {start} uses size=0 but is not the "
                "last box in its container",
                start,
            )
        size = len(view) - rel
        header_size = 8
    else:
        size = size32
        header_size = 8
        if size < 8:
            raise ParseError(
                f"box '{box_type}' declares size {size} smaller than 8-byte header",
                start,
            )
    end = start + size
    view_end = base + len(view)
    if end > view_end:
        raise ParseError(
            f"box '{box_type}' at {start} ends at {end} but view ends at {view_end}",
            start,
        )
    return BoxHeader(box_type, start, header_size, size, end)


def iter_boxes(
    view: memoryview, base: int = 0, depth: int = 0
) -> Iterator[tuple[BoxHeader, memoryview]]:
    """Yield ``(header, payload_view)`` for every sibling box in ``view``."""

    if depth > limits.MAX_BOX_DEPTH:
        raise LimitError(
            f"box nesting exceeds {limits.MAX_BOX_DEPTH} levels", base
        )
    rel = 0
    total = len(view)
    while rel < total:
        if total - rel < 8:
            raise ParseError(
                f"truncated box header: {total - rel} trailing byte(s)",
                base + rel,
            )
        header = read_box_header(view, base, rel)
        payload = view[header.content_start - base : header.end - base]
        yield header, payload
        rel = header.end - base


def find_first(
    view: memoryview, box_type: str, base: int = 0, depth: int = 0
) -> tuple[BoxHeader, memoryview] | None:
    """Return the first sibling box of ``box_type`` in ``view`` or None."""

    for header, payload in iter_boxes(view, base, depth):
        if header.type == box_type:
            return header, payload
    return None


def find_all(
    view: memoryview, box_type: str, base: int = 0, depth: int = 0
) -> list[tuple[BoxHeader, memoryview]]:
    """Return every sibling box of ``box_type`` in ``view``."""

    return [
        (header, payload)
        for header, payload in iter_boxes(view, base, depth)
        if header.type == box_type
    ]


# Container box types whose payloads are themselves sequences of boxes.
_CONTAINER_TYPES = frozenset(
    {"moov", "trak", "mdia", "minf", "stbl", "edts", "dinf", "mvex"}
)


def find_deep(
    view: memoryview, box_type: str, base: int = 0, depth: int = 0
) -> tuple[BoxHeader, memoryview] | None:
    """Depth-first recursive search through known container boxes."""

    for header, payload in iter_boxes(view, base, depth):
        if header.type == box_type:
            return header, payload
        if header.type in _CONTAINER_TYPES:
            found = find_deep(
                payload, box_type, base=header.content_start, depth=depth + 1
            )
            if found is not None:
                return found
    return None


def fullbox_version(view: memoryview) -> int:
    """Read the version byte of a FullBox payload (flags are validated too)."""

    _need(view, 0, FULLBOX_HEADER)
    return view[0]


def fullbox_flags(view: memoryview) -> int:
    _need(view, 0, FULLBOX_HEADER)
    return (view[1] << 16) | (view[2] << 8) | view[3]


def bounded_view(view: memoryview, base: int, start: int, end: int) -> memoryview:
    """Return a memoryview for absolute ``[start, end)`` with bound checks."""

    if start < base or end < start or end > base + len(view):
        raise ParseError(
            f"bounded view [{start}, {end}) outside parent [{base}, {base + len(view)})",
            max(start, base),
        )
    return view[start - base : end - base]
