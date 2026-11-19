"""Generic EBML (Extensible Binary Meta Language) reader and writer helpers.

The reader works on a seekable binary file and keeps explicit parent
boundaries. Unknown (all-ones) element lengths are accepted only for caller
approved master elements and terminate at the next legal sibling.
"""

from __future__ import annotations

from dataclasses import dataclass

from . import MAX_ELEMENT_DEPTH, MAX_ELEMENT_SIZE

# VINT data-bit masks per octet count, e.g. 2 octets -> 0x3FFF.
_VINT_MASKS = [(1 << (7 * n)) - 1 for n in range(1, 9)]


class EbmlError(ValueError):
    """Raised on malformed EBML encoding or boundary violations."""


@dataclass(slots=True)
class Element:
    """A parsed EBML element header (payload is read on demand)."""

    id: int
    header_start: int
    data_start: int
    size: int | None  # None means an unknown-length element
    end: int | None
    depth: int

    @property
    def known_size(self) -> bool:
        return self.size is not None


def _vint_length(first_byte: int) -> int:
    if first_byte == 0:
        raise EbmlError("VINT starts with 0x00: no length marker")
    n = 1
    marker = 0x80
    while first_byte & marker == 0:
        marker >>= 1
        n += 1
        if n > 8:
            raise EbmlError("VINT longer than 8 octets")
    return n


class EbmlReader:
    """Cursor-based EBML reader over a seekable binary stream."""

    def __init__(self, stream, *, allowed_unknown_ids: set[int] | None = None):
        self._f = stream
        self._unknown_ids = allowed_unknown_ids or set()
        self._bounds: list[int | None] = []  # end offsets; None = unknown length

    # -- primitives -------------------------------------------------------

    def pos(self) -> int:
        return self._f.tell()

    def seek(self, offset: int) -> None:
        self._f.seek(offset)

    def _read_exact(self, n: int) -> bytes:
        data = self._f.read(n)
        if len(data) != n:
            raise EbmlError(f"truncated element: wanted {n} bytes, got {len(data)}")
        return data

    def read_vint(self, *, allow_unknown: bool = False) -> tuple[int | None, int]:
        """Return (value, octet_count); value is None for an unknown length."""
        start = self.pos()
        first = self._read_exact(1)[0]
        n = _vint_length(first)
        raw = self._read_exact(n - 1)
        value = first & _VINT_MASKS[n - 1]
        for byte in raw:
            value = (value << 8) | byte
        if value == _VINT_MASKS[n - 1]:
            if not allow_unknown:
                raise EbmlError(f"unknown-length VINT not allowed at offset {start}")
            return None, n
        return value, n

    # -- element headers --------------------------------------------------

    def read_element_header(self, depth: int) -> Element:
        if depth > MAX_ELEMENT_DEPTH:
            raise EbmlError(f"element nesting exceeds {MAX_ELEMENT_DEPTH}")
        header_start = self.pos()
        eid, id_len = self.read_vint()
        # Reserved EBML IDs have every data bit set.
        if eid == _VINT_MASKS[id_len - 1]:
            raise EbmlError(f"reserved EBML ID at offset {header_start}")
        size, size_len = self.read_vint(
            allow_unknown=eid in self._unknown_ids
        )
        data_start = header_start + id_len + size_len
        if size is not None:
            if size > MAX_ELEMENT_SIZE:
                raise EbmlError(
                    f"element {eid:#x} size {size} exceeds limit {MAX_ELEMENT_SIZE}"
                )
            end = data_start + size
            for bound in self._bounds:
                if bound is not None and end > bound:
                    raise EbmlError(
                        f"element {eid:#x} ends at {end}, past parent boundary {bound}"
                    )
        else:
            end = None
        return Element(eid, header_start, data_start, size, end, depth)

    def push_bound(self, element: Element) -> None:
        if element.known_size:
            self.seek(element.end)  # sanity: bounded parent fully consumed later
            self.seek(element.data_start)
        self._bounds.append(element.end)

    def pop_bound(self) -> None:
        self._bounds.pop()

    def iter_children(
        self,
        parent: Element,
        *,
        terminator_ids: set[int] | None = None,
    ):
        """Yield direct child elements, honouring parent boundaries.

        For unknown-length parents a child whose ID is in ``terminator_ids``
        ends the parent; the stream is left at that sibling's header.
        """
        self.push_bound(parent)
        terminator_ids = terminator_ids or set()
        try:
            while True:
                if parent.end is not None:
                    if self.pos() > parent.end:
                        raise EbmlError("read past parent element boundary")
                    if self.pos() == parent.end:
                        return
                header_pos = self.pos()
                peek = self._f.read(1)
                if not peek:
                    if parent.end is None:
                        return  # unknown-length parent ends at EOF
                    raise EbmlError("truncated parent element before boundary")
                self.seek(header_pos)
                child = self.read_element_header(parent.depth + 1)
                if parent.end is None and child.id in terminator_ids:
                    self.seek(header_pos)
                    return
                yield child
                # Skip to the end of the child payload if caller didn't read it.
                if child.known_size and self.pos() != child.end:
                    if self.pos() > child.end:
                        raise EbmlError("reader overran child element payload")
                    self.seek(child.end)
        finally:
            self.pop_bound()

    # -- payload readers --------------------------------------------------

    def read_payload(self, element: Element) -> bytes:
        if element.size is None:
            raise EbmlError("cannot bulk-read an unknown-length element")
        self.seek(element.data_start)
        data = self._read_exact(element.size)
        self.seek(element.end)
        return data

    def read_uint(self, element: Element) -> int:
        data = self.read_payload(element)
        return int.from_bytes(data, "big", signed=False)

    def read_sint(self, element: Element) -> int:
        data = self.read_payload(element)
        return int.from_bytes(data, "big", signed=True)

    def read_float(self, element: Element) -> float:
        data = self.read_payload(element)
        if len(data) == 4:
            import struct

            return struct.unpack(">f", data)[0]
        if len(data) == 8:
            import struct

            return struct.unpack(">d", data)[0]
        raise EbmlError(f"float element must be 4 or 8 bytes, got {len(data)}")

    def read_string(self, element: Element) -> str:
        return self.read_payload(element).rstrip(b"\x00").decode("utf-8", "replace")

    def read_binary(self, element: Element) -> bytes:
        return self.read_payload(element)


# --------------------------------------------------------------------------
# Writer side
# --------------------------------------------------------------------------


def encode_id(eid: int) -> bytes:
    """Encode an EBML ID constant (already carries VINT marker bits)."""
    if eid <= 0:
        raise EbmlError(f"invalid EBML ID {eid!r}")
    for n in range(1, 5):
        if eid < 1 << (8 * n):
            encoded = eid.to_bytes(n, "big")
            # The leading byte must contain a VINT length marker.
            if _vint_length(encoded[0]) != n:
                raise EbmlError(f"ID {eid:#x} lacks a legal VINT marker")
            return encoded
    raise EbmlError(f"EBML ID {eid:#x} too long")


def encode_vint(value: int, num_octets: int | None = None) -> bytes:
    """Encode an unsigned integer as an EBML VINT (never unknown-length)."""
    if value < 0:
        raise EbmlError("VINT cannot encode negative values")
    if num_octets is None:
        num_octets = 1
        while value >= _VINT_MASKS[num_octets - 1]:  # all-ones is reserved
            num_octets += 1
            if num_octets > 8:
                raise EbmlError("VINT value too large")
    if value >= _VINT_MASKS[num_octets - 1]:
        raise EbmlError(f"value {value} does not fit in {num_octets}-octet VINT")
    marker = 1 << (8 * num_octets - 1)
    return (marker | value).to_bytes(num_octets, "big")


def unknown_size_vint(num_octets: int) -> bytes:
    """Return the all-ones unknown-length VINT header of the given width."""
    if not 1 <= num_octets <= 8:
        raise EbmlError("VINT octet count must be 1..8")
    return b"\xff" if num_octets == 1 else (b"\x7f" + b"\xff" * (num_octets - 1))


def encode_element(eid: int, payload: bytes) -> bytes:
    return encode_id(eid) + encode_vint(len(payload)) + payload


def encode_uint_element(eid: int, value: int) -> bytes:
    if value < 0:
        raise EbmlError("uint element cannot be negative")
    width = max(1, (value.bit_length() + 7) // 8)
    return encode_element(eid, value.to_bytes(width, "big"))
