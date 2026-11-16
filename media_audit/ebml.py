"""Bounded Matroska EBML element reader.

Implements just enough of the EBML encoding (RFC 8794 style variable-length
integers and nested elements) to walk a Matroska segment with hard limits on
element count, nesting depth, integer width and read size.
"""

from __future__ import annotations

import os
import struct
from dataclasses import dataclass

DEFAULT_LIMITS: dict[str, int] = {
    "max_file_bytes": 8 * 1024 * 1024 * 1024,
    "max_elements": 200_000,
    "max_depth": 32,
    "max_id_width": 4,
    "max_size_width": 8,
    "max_payload_read": 16 * 1024 * 1024,
}

# Matroska element identifiers used while walking. Keeping the numeric IDs in
# one place makes the probe logic read like the Matroska spec.
ID_EBML = 0x1A45DFA3
ID_SEGMENT = 0x18538067
ID_INFO = 0x1549A966
ID_TRACKS = 0x1654AE6B
ID_TRACK_ENTRY = 0xAE
ID_CLUSTER = 0x1F43B675


class EBMLError(Exception):
    """Raised on a malformed element or a violated hard limit."""

    def __init__(self, message: str, offset: int | None = None) -> None:
        super().__init__(message)
        self.offset = offset


@dataclass(frozen=True)
class Element:
    """One EBML element header plus its payload span."""

    id: int
    start: int
    header_size: int
    payload_start: int
    size: int | None  # ``None`` marks an unknown-size (all-ones) element
    end: int
    depth: int

    @property
    def payload_end(self) -> int:
        return self.end


def _vint_width(first_byte: int) -> int:
    """Number of bytes in a VINT given its leading byte."""
    if first_byte == 0:
        return 0  # caller turns this into an error
    width = 1
    mask = 0x80
    while first_byte & mask == 0:
        width += 1
        mask >>= 1
    return width


class EBMLReader:
    """Bounded random-access reader over one EBML/Matroska file."""

    def __init__(self, path: str, limits: dict[str, int] | None = None) -> None:
        self.path = path
        self.limits = dict(DEFAULT_LIMITS)
        if limits:
            self.limits.update(limits)
        self._f = open(path, "rb")
        self.file_size = os.fstat(self._f.fileno()).st_size
        if self.file_size > self.limits["max_file_bytes"]:
            self.close()
            raise EBMLError(
                f"file size {self.file_size} exceeds "
                f"{self.limits['max_file_bytes']} byte limit"
            )
        self._element_count = 0

    def close(self) -> None:
        self._f.close()

    def __enter__(self) -> "EBMLReader":
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()

    def read(self, offset: int, length: int) -> bytes:
        if length < 0 or offset < 0:
            raise EBMLError("negative read request", offset)
        if length > self.limits["max_payload_read"]:
            raise EBMLError(
                f"read of {length} bytes exceeds payload read limit", offset
            )
        if offset + length > self.file_size:
            raise EBMLError(
                f"truncated read at offset {offset}, wanted {length} bytes, "
                f"only {max(0, self.file_size - offset)} available",
                offset,
            )
        self._f.seek(offset)
        data = self._f.read(length)
        if len(data) != length:
            raise EBMLError("short read", offset)
        return data

    def read_element_id(self, offset: int, boundary: int) -> tuple[int, int]:
        """Read an element ID VINT keeping its length-marker bits.

        Matroska IDs are conventionally written with the marker included
        (EBML header = ``0x1A45DFA3``), so unlike size VINTs the marker is
        not stripped here.
        """
        if offset >= boundary:
            raise EBMLError("unexpected end while reading element ID", offset)
        first = self.read(offset, 1)[0]
        width = _vint_width(first)
        if width == 0:
            raise EBMLError("element ID with zero width", offset)
        if width > self.limits["max_id_width"]:
            raise EBMLError(
                f"element ID width {width} exceeds limit "
                f"{self.limits['max_id_width']}",
                offset,
            )
        raw = self.read(offset, width)
        value = int.from_bytes(raw, "big")
        return value, width

    def read_vint(
        self, offset: int, max_width: int, boundary: int
    ) -> tuple[int, int, bool]:
        """Return ``(value, width, all_ones)`` for a VINT at ``offset``."""
        if offset >= boundary:
            raise EBMLError("unexpected end while reading VINT", offset)
        first = self.read(offset, 1)[0]
        width = _vint_width(first)
        if width == 0:
            raise EBMLError("VINT with zero width (leading 0x00 byte)", offset)
        if width > max_width:
            raise EBMLError(
                f"VINT width {width} exceeds limit {max_width}", offset
            )
        raw = self.read(offset, width)
        value = 0
        for i, byte in enumerate(raw):
            if i == 0:
                byte &= (1 << (8 - width)) - 1  # strip length marker
            value = (value << 8) | byte
        # A VINT of width w carries 7*w data bits; an all-ones data part is
        # the reserved "unknown length" encoding.
        all_ones = width <= 8 and value == (1 << (7 * width)) - 1
        return value, width, all_ones

    def iter_children(self, start: int, end: int, depth: int = 0):
        offset = start
        while offset < end:
            if depth > self.limits["max_depth"]:
                raise EBMLError(
                    f"element nesting deeper than {self.limits['max_depth']}",
                    offset,
                )
            self._element_count += 1
            if self._element_count > self.limits["max_elements"]:
                raise EBMLError(
                    f"element count exceeds {self.limits['max_elements']}",
                    offset,
                )
            elem_id, id_width = self.read_element_id(offset, end)
            size_offset = offset + id_width
            size, size_width, unknown_size = self.read_vint(
                size_offset, self.limits["max_size_width"], end
            )
            header_size = id_width + size_width
            payload_start = offset + header_size
            if unknown_size:
                elem_end = end  # unknown size: bounded by parent, per spec
            else:
                elem_end = payload_start + size
                if elem_end > end:
                    raise EBMLError(
                        f"element 0x{elem_id:X} at {offset} ends at {elem_end}, "
                        f"beyond parent boundary {end}",
                        offset,
                    )
            yield Element(
                id=elem_id,
                start=offset,
                header_size=header_size,
                payload_start=payload_start,
                size=None if unknown_size else size,
                end=elem_end,
                depth=depth,
            )
            offset = elem_end

    def payload(self, element: Element, max_length: int | None = None) -> bytes:
        length = element.payload_end - element.payload_start
        if max_length is not None and length > max_length:
            raise EBMLError(
                f"payload of 0x{element.id:X} is {length} bytes, "
                f"limit {max_length}",
                element.payload_start,
            )
        return self.read(element.payload_start, length)

    # --- typed leaf readers -------------------------------------------------

    def read_uint(self, element: Element) -> int:
        data = self.payload(element, 8)
        return int.from_bytes(data, "big") if data else 0

    def read_float(self, element: Element) -> float:
        data = self.payload(element, 8)
        if len(data) == 4:
            return struct.unpack(">f", data)[0]
        if len(data) == 8:
            return struct.unpack(">d", data)[0]
        raise EBMLError(
            f"float element 0x{element.id:X} has width {len(data)}",
            element.payload_start,
        )

    def read_string(self, element: Element) -> str:
        data = self.payload(element, 4096)
        return data.split(b"\x00", 1)[0].decode("ascii", errors="replace")

    def read_utf8(self, element: Element) -> str:
        data = self.payload(element, 4096)
        return data.split(b"\x00", 1)[0].decode("utf-8", errors="replace")
