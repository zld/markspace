"""Minimal, bounded ISO BMFF (MP4/MOV) box hierarchy parser.

The parser never loads a whole file. Each read is bounded by the declaring
box size and a set of hard limits so that a malicious or truncated file cannot
trigger unbounded allocation or recursion.
"""

from __future__ import annotations

import os
import struct
from dataclasses import dataclass

# Container boxes whose payload is itself a sequence of full boxes.
CONTAINER_BOXES = frozenset(
    {
        b"moov",
        b"trak",
        b"mdia",
        b"minf",
        b"stbl",
        b"udta",
        b"edts",
        b"mvex",
        b"moof",
        b"traf",
        b"mfra",
        b"skip",
        b"meco",
    }
)

# 'meta' is historically a plain container in some files and a FullBox
# (version/flags prefix) in others; callers use :data:`META_BOX` to special
# case it.
META_BOX = b"meta"

DEFAULT_LIMITS: dict[str, int] = {
    "max_file_bytes": 8 * 1024 * 1024 * 1024,
    "max_boxes": 200_000,
    "max_depth": 32,
    "max_payload_read": 16 * 1024 * 1024,
    "max_header_size": 64 * 1024 * 1024,
}


class IsoBmffError(Exception):
    """Raised when a box structure is truncated or violates a hard limit."""

    def __init__(self, message: str, offset: int | None = None) -> None:
        super().__init__(message)
        self.offset = offset


@dataclass(frozen=True)
class Box:
    """One parsed box header plus its payload span."""

    type: bytes
    start: int
    header_size: int
    payload_start: int
    size: int  # full box size including header; 0 marks a zero-length tail box
    depth: int

    @property
    def end(self) -> int:
        return self.start + self.size

    @property
    def payload_end(self) -> int:
        return self.end


class IsoBmffReader:
    """Bounded random-access reader over one ISO BMFF file."""

    def __init__(self, path: str, limits: dict[str, int] | None = None) -> None:
        self.path = path
        self.limits = dict(DEFAULT_LIMITS)
        if limits:
            self.limits.update(limits)
        self._f = open(path, "rb")
        self.file_size = os.fstat(self._f.fileno()).st_size
        if self.file_size > self.limits["max_file_bytes"]:
            self.close()
            raise IsoBmffError(
                f"file size {self.file_size} exceeds "
                f"{self.limits['max_file_bytes']} byte limit"
            )
        self._box_count = 0

    def close(self) -> None:
        self._f.close()

    def __enter__(self) -> "IsoBmffReader":
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()

    def read(self, offset: int, length: int) -> bytes:
        """Read exactly ``length`` bytes, rejecting out-of-range requests."""
        if length < 0 or offset < 0:
            raise IsoBmffError("negative read request", offset)
        if length > self.limits["max_payload_read"]:
            raise IsoBmffError(
                f"read of {length} bytes exceeds payload read limit", offset
            )
        if offset + length > self.file_size:
            raise IsoBmffError(
                f"truncated read at offset {offset}, wanted {length} bytes, "
                f"only {max(0, self.file_size - offset)} available",
                offset,
            )
        self._f.seek(offset)
        data = self._f.read(length)
        if len(data) != length:
            raise IsoBmffError("short read", offset)
        return data

    def iter_children(
        self, start: int, end: int, depth: int = 0
    ):
        """Yield every box header spanning ``[start, end)``.

        Recursion is handled by the caller (via :data:`CONTAINER_BOXES`) so
        that the depth/box-count limits are enforced in one place.
        """
        offset = start
        while offset < end:
            if depth > self.limits["max_depth"]:
                raise IsoBmffError(
                    f"box nesting deeper than {self.limits['max_depth']} levels",
                    offset,
                )
            self._box_count += 1
            if self._box_count > self.limits["max_boxes"]:
                raise IsoBmffError(
                    f"box count exceeds {self.limits['max_boxes']}", offset
                )
            header = self.read(offset, min(8, end - offset))
            if len(header) < 8:
                raise IsoBmffError("truncated box header", offset)
            size32, box_type = struct.unpack(">I4s", header)
            header_size = 8
            if size32 == 1:  # 64-bit extended size
                ext = self.read(offset + 8, 8)
                (size64,) = struct.unpack(">Q", ext)
                header_size = 16
                box_size = size64
            elif size32 == 0:  # box extends to end of file / parent span
                box_size = end - offset
                if box_size == 0:
                    return
            else:
                box_size = size32
            if box_size < header_size:
                raise IsoBmffError(
                    f"box {box_type!r} size {box_size} smaller than its header",
                    offset,
                )
            if header_size > self.limits["max_header_size"]:
                raise IsoBmffError("box header exceeds header size limit", offset)
            box_end = offset + box_size
            if box_end > end:
                raise IsoBmffError(
                    f"box {box_type!r} at {offset} ends at {box_end}, beyond "
                    f"parent boundary {end}",
                    offset,
                )
            yield Box(
                type=box_type,
                start=offset,
                header_size=header_size,
                payload_start=offset + header_size,
                size=box_size,
                depth=depth,
            )
            if size32 == 0:
                return  # zero-length tail box always consumes the remainder
            offset = box_end

    def payload(self, box: Box, max_length: int | None = None) -> bytes:
        length = box.payload_end - box.payload_start
        if max_length is not None and length > max_length:
            raise IsoBmffError(
                f"payload of {box.type!r} is {length} bytes, limit {max_length}",
                box.payload_start,
            )
        return self.read(box.payload_start, length)

    def walk(self, start: int = 0, end: int | None = None, depth: int = 0):
        """Depth-first walk yielding every :class:`Box` in the file."""
        if end is None:
            end = self.file_size
        for box in self.iter_children(start, end, depth):
            yield box
            if box.type in CONTAINER_BOXES:
                yield from self.walk(box.payload_start, box.payload_end, depth + 1)
            elif box.type == META_BOX:
                # Skip the optional 4-byte FullBox version/flags prefix.
                yield from self.walk(
                    box.payload_start + 4, box.payload_end, depth + 1
                )
