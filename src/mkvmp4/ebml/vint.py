"""EBML variable-length integers (VINT): element IDs and size descriptors.

A VINT is 1..8 octets. The leading zero bit of the first octet gives the
length; the remaining bits are the payload. An all-ones payload marks the
"unknown length" sentinel used by streaming Matroska writers.
"""

from collections.abc import Iterable

from ..errors import CorruptMKVError

#: Maximum legal VINT width (EBML allows 8 octets).
MAX_VINT_WIDTH = 8
#: First-octet marker for each width: 1 << (8 - width).
_LENGTH_MARKER = (0x80, 0x40, 0x20, 0x10, 0x08, 0x04, 0x02, 0x01)
#: Payload mask for each width.
_PAYLOAD_MASK = (0x7F, 0x3F, 0x1F, 0x0F, 0x07, 0x03, 0x01, 0x00)
#: All-ones payload per width -> unknown length.
_UNKNOWN_SENTINEL = (
    0x7F, 0x3FFF, 0x1FFFFF, 0x0FFFFFFF,
    0x07FFFFFFFF, 0x03FFFFFFFFFF, 0x01FFFFFFFFFFFF, 0x00FFFFFFFFFFFFFF,
)
#: Largest representable value for a width (unknown sentinel excluded).
_MAX_VALUE = (
    0x7E, 0x3FFE, 0x1FFFFE, 0x0FFFFFFE,
    0x07FFFFFFFE, 0x03FFFFFFFFFE, 0x01FFFFFFFFFFFE, 0x00FFFFFFFFFFFFFE,
)


def vint_width(first_octet: int, offset: int = 0) -> int:
    """Return the VINT length (1..8) implied by ``first_octet``.

    Raises :class:`CorruptMKVError` for an all-zero octet, which never starts
    a legal VINT.
    """
    for width, marker in enumerate(_LENGTH_MARKER, start=1):
        if first_octet & marker:
            return width
    raise CorruptMKVError(
        "illegal VINT: leading octet 0x00 has no length marker",
        offset=offset)


def decode_vint(data: bytes | bytearray | memoryview, offset: int = 0) -> tuple[int, int, int]:
    """Decode a VINT.

    Returns ``(value, width, next_offset)``. ``value`` is ``-1`` for the
    unknown-length sentinel. Raises :class:`CorruptMKVError` on truncation
    or an illegal leading octet; the reported offset is the VINT start so
    error messages carry the failing byte position.
    """
    if offset >= len(data):
        raise CorruptMKVError("truncated VINT: no bytes available", offset=offset)
    first = data[offset]
    width = vint_width(first, offset)
    end = offset + width
    if end > len(data):
        raise CorruptMKVError(
            f"truncated VINT: need {width} octet(s), found {len(data) - offset}",
            offset=offset)
    value = first & _PAYLOAD_MASK[width - 1]
    for i in range(offset + 1, end):
        value = (value << 8) | data[i]
    if value == _UNKNOWN_SENTINEL[width - 1]:
        return -1, width, end
    return value, width, end


def is_unknown_length(first_octet: int, data: bytes, offset: int) -> bool:
    """Check whether the size VINT at ``offset`` is an unknown-length one."""
    width = vint_width(first_octet, offset)
    payload = first_octet & _PAYLOAD_MASK[width - 1]
    for i in range(1, width):
        payload = (payload << 8) | data[offset + i]
    return payload == _UNKNOWN_SENTINEL[width - 1]


def encode_vint(value: int, width: int | None = None) -> bytes:
    """Encode ``value`` (non-negative) as a VINT.

    With ``width=None`` the smallest width that holds the value is chosen.
    Note: because the all-ones payload is reserved, a width whose range is
    fully consumed overflows into the next width (e.g. 0x7E fits in 1 octet,
    0x7F needs 2).
    """
    if value < 0:
        raise ValueError("VINT values must be non-negative")
    if width is None:
        for candidate in range(1, MAX_VINT_WIDTH + 1):
            if value <= _MAX_VALUE[candidate - 1]:
                width = candidate
                break
        if width is None:
            raise ValueError("value exceeds maximum VINT range")
    else:
        if not 1 <= width <= MAX_VINT_WIDTH:
            raise ValueError("VINT width must be between 1 and 8")
        if value > _MAX_VALUE[width - 1]:
            raise ValueError(f"value {value} does not fit in VINT width {width}")
    octets = bytearray(width)
    remaining = value
    for i in range(width - 1, 0, -1):
        octets[i] = remaining & 0xFF
        remaining >>= 8
    octets[0] = remaining | _LENGTH_MARKER[width - 1]
    return bytes(octets)


def encode_element_id(element_id: int) -> bytes:
    """Encode an EBML element ID, preserving its declared VINT width.

    Element IDs have a fixed width defined by the schema, so the width is
    derived from the ID's top set bits rather than minimized.
    """
    if element_id <= 0 or element_id.bit_length() > 32:
        raise ValueError("EBML element IDs are at most 4 octets wide")
    # The width marker is the highest set bit of the ID; its byte position
    # gives the declared 1..4 octet width (e.g. 0xEC -> 1, 0x1F43B675 -> 4).
    width = (element_id.bit_length() - 1) // 8 + 1
    if not 1 <= width <= 4:
        raise ValueError("EBML element IDs are at most 4 octets wide")
    # Element IDs are not size VINTs: the all-ones payload is not reserved
    # here (e.g. Cluster ID 0x1F43B675), so encode without range checks.
    octets = bytearray(width)
    remaining = element_id
    for i in range(width - 1, -1, -1):
        octets[i] = remaining & 0xFF
        remaining >>= 8
    return bytes(octets)


def encode_unknown_size(width: int = 1) -> bytes:
    """Encode an unknown-length size VINT (all payload bits set)."""
    if not 1 <= width <= MAX_VINT_WIDTH:
        raise ValueError("VINT width must be between 1 and 8")
    return bytes([_LENGTH_MARKER[width - 1] | _PAYLOAD_MASK[width - 1]]) + b"\xff" * (width - 1)


def iter_vints(data: bytes) -> Iterable[tuple[int, int]]:
    """Yield ``(value, offset)`` for every VINT in ``data`` (test helper)."""
    offset = 0
    while offset < len(data):
        value, width, next_offset = decode_vint(data, offset)
        yield value, offset
        offset = next_offset
