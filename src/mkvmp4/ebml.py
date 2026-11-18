"""EBML variable-length integers and element headers.

Every parse failure raises :class:`EbmlError` carrying the byte offset so
callers can report exactly where the input is corrupt.
"""
from __future__ import annotations

MAX_ID_WIDTH = 4
MAX_SIZE_WIDTH = 8
UNKNOWN_SIZE = -1  # all-ones size value ("unknown length element")


class EbmlError(Exception):
    """EBML structure error; ``offset`` is the byte position of the fault."""

    def __init__(self, message: str, offset: int):
        super().__init__(f"{message} (at byte offset {offset})")
        self.message = message
        self.offset = offset


def _vint_width(first: int, maximum: int, what: str, offset: int) -> int:
    if first == 0:
        raise EbmlError(f"{what} has zero first byte (invalid width)", offset)
    mask = 0x80
    width = 1
    while width <= maximum and not (first & mask):
        mask >>= 1
        width += 1
    if width > maximum:
        raise EbmlError(f"{what} width {width} exceeds {maximum} bytes", offset)
    return width


def read_id(data: bytes, pos: int) -> tuple[int, int]:
    """Read an element ID at ``pos`` (marker bit retained).

    Returns ``(id_value, width)``. IDs are at most 4 bytes wide.
    """
    if pos >= len(data):
        raise EbmlError("truncated element ID", pos)
    width = _vint_width(data[pos], MAX_ID_WIDTH, "element ID", pos)
    if pos + width > len(data):
        raise EbmlError("truncated element ID", pos)
    return int.from_bytes(data[pos:pos + width], "big"), width


def read_size(data: bytes, pos: int, allow_unknown: bool = True) -> tuple[int, int]:
    """Read an element size at ``pos`` (marker bit stripped).

    Returns ``(value, width)``; value is ``UNKNOWN_SIZE`` for the all-ones
    form when ``allow_unknown`` is true, otherwise that form is rejected.
    """
    if pos >= len(data):
        raise EbmlError("truncated element size", pos)
    width = _vint_width(data[pos], MAX_SIZE_WIDTH, "element size", pos)
    if pos + width > len(data):
        raise EbmlError("truncated element size", pos)
    value = int.from_bytes(data[pos:pos + width], "big") & ((1 << (7 * width)) - 1)
    if value == (1 << (7 * width)) - 1:
        if not allow_unknown:
            raise EbmlError("unknown (all-ones) size not allowed here", pos)
        return UNKNOWN_SIZE, width
    return value, width


def read_header(data: bytes, pos: int, allow_unknown: bool = True) -> tuple[int, int, int]:
    """Read an element header: returns ``(id, size, header_width)``."""
    eid, id_width = read_id(data, pos)
    size, size_width = read_size(data, pos + id_width, allow_unknown=allow_unknown)
    return eid, size, id_width + size_width
