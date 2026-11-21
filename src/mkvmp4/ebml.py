"""EBML primitive decoding: variable-length integers and element headers.

Implemented from the EBML specification (RFC 8794) rather than a container
library.  All readers take an absolute base offset so every failure can be
reported against the original input byte stream.
"""

from __future__ import annotations

from dataclasses import dataclass

from .errors import EbmlParseError

# Split at 1 << 56 so that "no 8-byte VINT can carry this value" is decided in
# Python integers before any unvalidated data can become a NumPy scalar.
_VINT56 = 1 << 56


@dataclass(frozen=True)
class ElementHeader:
    """A decoded EBML element header located at ``offset``."""

    element_id: int
    offset: int  # first byte of the ID
    id_len: int
    size_len: int
    size: int | None  # None means "unknown size"
    data_offset: int  # first byte of the element data


def read_vint_id(data: bytes, pos: int, *, base: int = 0, path: str = "") -> tuple[int, int]:
    """Decode an EBML element ID including its length-marker bit.

    IDs are stored with the marker bit retained (e.g. Segment is 0x18538067),
    because the length of the ID is part of its identity.
    """
    if pos >= len(data):
        raise EbmlParseError(
            "truncated element ID: end of data reached",
            offset=base + pos,
            path=path,
        )
    first = data[pos]
    if first == 0:
        raise EbmlParseError(
            "invalid VINT: ID starts with a zero byte",
            offset=base + pos,
            path=path,
        )
    length = 9 - first.bit_length()
    if length > 4:
        raise EbmlParseError(
            f"invalid EBML ID length {length} (maximum 4)",
            offset=base + pos,
            path=path,
        )
    if pos + length > len(data):
        raise EbmlParseError(
            f"truncated element ID: need {length} bytes, have {len(data) - pos}",
            offset=base + pos,
            path=path,
        )
    return int.from_bytes(data[pos : pos + length], "big"), length


def read_vint_size(
    data: bytes,
    pos: int,
    *,
    base: int = 0,
    path: str = "",
) -> tuple[int | None, int, bool]:
    """Decode an EBML data size.

    Returns ``(size, length, unknown)``.  ``unknown`` is true for the reserved
    all-ones value, which is how Matroska marks live/streamed elements with
    indeterminate length.
    """
    if pos >= len(data):
        raise EbmlParseError(
            "truncated element size: end of data reached",
            offset=base + pos,
            path=path,
        )
    first = data[pos]
    if first == 0:
        raise EbmlParseError(
            "invalid VINT: size field starts with a zero byte",
            offset=base + pos,
            path=path,
        )
    length = 9 - first.bit_length()
    if pos + length > len(data):
        raise EbmlParseError(
            f"truncated element size: need {length} bytes, have {len(data) - pos}",
            offset=base + pos,
            path=path,
        )
    value = first & (0xFF >> length)
    for byte in data[pos + 1 : pos + length]:
        value = (value << 8) | byte
    marker = (1 << (7 * length)) - 1
    if value == marker:
        if length == 8 and (data[pos] & 0x7F) != 0:
            # An 8-byte VINT with a non-zero payload bit cannot be the
            # all-ones reserved pattern; it is a value we cannot represent.
            raise EbmlParseError(
                "invalid 8-byte VINT size: value does not fit in 56 bits",
                offset=base + pos,
                path=path,
            )
        return None, length, True
    if value >= _VINT56:
        raise EbmlParseError(
            "invalid 8-byte VINT size: value does not fit in 56 bits",
            offset=base + pos,
            path=path,
        )
    return value, length, False


def read_element_header(
    data: bytes,
    pos: int,
    *,
    base: int = 0,
    path: str = "",
    limit: int | None = None,
) -> ElementHeader:
    """Decode one element header and verify it stays within ``limit``.

    ``limit`` is the exclusive upper bound of the enclosing region (the end of
    the file for top-level elements, or the parent's data end).  A declared
    size that crosses it is rejected here instead of being sliced later.
    """
    end = len(data) if limit is None else limit
    if pos >= end:
        raise EbmlParseError(
            "expected an element header but the enclosing region is exhausted",
            offset=base + pos,
            path=path,
        )
    element_id, id_len = read_vint_id(data, pos, base=base, path=path)
    size, size_len, unknown = read_vint_size(data, pos + id_len, base=base, path=path)
    data_offset = pos + id_len + size_len
    if data_offset > end:
        raise EbmlParseError(
            "element header crosses its enclosing region",
            offset=base + pos,
            path=path,
        )
    effective = None if unknown else size
    if effective is not None and data_offset + effective > end:
        raise EbmlParseError(
            f"element size {effective} overruns its region by "
            f"{data_offset + effective - end} bytes",
            offset=base + pos,
            path=path,
        )
    return ElementHeader(
        element_id=element_id,
        offset=pos,
        id_len=id_len,
        size_len=size_len,
        size=effective,
        data_offset=data_offset,
    )


def decode_uint(data: bytes, start: int, length: int, *, base: int = 0, path: str = "") -> int:
    """Unsigned big-endian integer; 0..8 bytes, empty means zero."""
    if length < 0 or start + length > len(data):
        raise EbmlParseError("integer field is truncated", offset=base + start, path=path)
    if length > 8:
        raise EbmlParseError(
            f"integer field of {length} bytes exceeds 8",
            offset=base + start,
            path=path,
        )
    return int.from_bytes(data[start : start + length], "big")


def decode_int(data: bytes, start: int, length: int, *, base: int = 0, path: str = "") -> int:
    """Two's-complement signed big-endian integer; empty means zero."""
    if length < 0 or start + length > len(data):
        raise EbmlParseError("signed integer field is truncated", offset=base + start, path=path)
    if length > 8:
        raise EbmlParseError(
            f"signed integer field of {length} bytes exceeds 8",
            offset=base + start,
            path=path,
        )
    return int.from_bytes(data[start : start + length], "big", signed=True)


def decode_float(data: bytes, start: int, length: int, *, base: int = 0, path: str = "") -> float:
    """IEEE-754 float stored in 0, 4 or 8 bytes (0 means 0.0)."""
    import struct

    if length == 0:
        return 0.0
    if start + length > len(data):
        raise EbmlParseError("float field is truncated", offset=base + start, path=path)
    if length == 4:
        return struct.unpack(">f", data[start : start + length])[0]
    if length == 8:
        return struct.unpack(">d", data[start : start + length])[0]
    raise EbmlParseError(
        f"unsupported float width {length}",
        offset=base + start,
        path=path,
    )


def decode_string(data: bytes, start: int, length: int) -> str:
    """ASCII/UTF-8 string with trailing NUL padding removed."""
    return data[start : start + length].rstrip(b"\x00").decode("utf-8", "replace")
