"""Parse and encode version 0/1 segment index (``sidx``) boxes.

Reference targets are resolved against the absolute byte offset of the sidx
box: the first referenced byte is ``box_end + first_offset`` and every later
reference starts where the previous one ended.  Only media references are
accepted; hierarchical (nested sidx) layouts and encrypted sample flags are
rejected by the callers that own those policy decisions.
"""

from __future__ import annotations

import struct

from .box_reader import Box, Cursor, find_all
from .errors import StructureError
from .model import Sidx, SidxReference

MAX_REFERENCES = 0xFFFF
_REF = struct.Struct(">III")


def parse_sidx(view: memoryview, box: Box) -> Sidx:
    cursor = Cursor(view, box.payload_start, box.end, box.type, box.start)
    version = cursor.u8()
    flags = (cursor.u8() << 16) | (cursor.u8() << 8) | cursor.u8()
    if version not in (0, 1):
        raise StructureError(
            f"unsupported sidx version {version}",
            box_type=b"sidx",
            offset=box.start,
        )
    if flags:
        raise StructureError(
            f"sidx flags 0x{flags:06x} are not supported",
            box_type=b"sidx",
            offset=box.start,
        )
    reference_id = cursor.u32()
    timescale = cursor.u32()
    if timescale == 0:
        raise StructureError(
            "sidx timescale is zero", box_type=b"sidx", offset=box.start
        )
    if version == 0:
        earliest = cursor.u32()
        first_offset = cursor.u32()
    else:
        earliest = cursor.u64()
        first_offset = cursor.u64()
    cursor.skip(2)  # reserved
    reference_count = cursor.u16()
    if reference_count > MAX_REFERENCES:
        raise StructureError(
            f"sidx reference count {reference_count} exceeds {MAX_REFERENCES}",
            box_type=b"sidx",
            offset=box.start,
        )
    expected = reference_count * _REF.size
    if cursor.remaining != expected:
        raise StructureError(
            f"sidx declares {reference_count} references ({expected} bytes) "
            f"but {cursor.remaining} payload bytes remain",
            box_type=b"sidx",
            offset=box.start,
        )
    references: list[SidxReference] = []
    for index in range(reference_count):
        word, duration, sap = _REF.unpack(cursor.take(_REF.size))
        reference_type = (word >> 31) & 1
        referenced_size = word & 0x7FFFFFFF
        if referenced_size == 0:
            raise StructureError(
                f"sidx reference {index} has a zero referenced_size",
                box_type=b"sidx",
                offset=box.start,
            )
        references.append(
            SidxReference(
                index=index,
                reference_type=reference_type,
                referenced_size=referenced_size,
                subsegment_duration=duration,
                starts_with_sap=(sap >> 31) & 1,
                sap_type=(sap >> 28) & 0x7,
                sap_delta_time=sap & 0x0FFFFFFF,
            )
        )
    return Sidx(
        version=version,
        reference_id=reference_id,
        timescale=timescale,
        earliest_presentation_time=earliest,
        first_offset=first_offset,
        references=references,
        box_start=box.start,
        box_end=box.end,
    )


def find_sidx(view: memoryview, roots: list[Box], *, required: bool = False) -> Sidx | None:
    boxes = find_all(roots, b"sidx")
    if not boxes:
        if required:
            raise StructureError("no top-level sidx box found", box_type=b"sidx")
        return None
    if len(boxes) > 1:
        raise StructureError(
            f"expected at most one top-level sidx box, found {len(boxes)}",
            box_type=b"sidx",
            offset=boxes[1].start,
        )
    return parse_sidx(view, boxes[0])


def reference_offsets(sidx: Sidx) -> list[tuple[int, int]]:
    """Absolute ``[start, end)`` byte range for each media reference."""

    position = sidx.box_end + sidx.first_offset
    ranges: list[tuple[int, int]] = []
    for reference in sidx.references:
        ranges.append((position, position + reference.referenced_size))
        position += reference.referenced_size
    return ranges


def encode_sidx(
    *,
    version: int,
    reference_id: int,
    timescale: int,
    earliest_presentation_time: int,
    first_offset: int,
    references: list[tuple[int, int, int]],
) -> bytes:
    """Serialize a sidx box.

    ``references`` is a list of ``(size, duration, sap_word)`` tuples.  The
    version is upgraded from 0 to 1 automatically when a value no longer fits
    the 32-bit fields; the caller therefore never has to guess.
    """

    if version not in (0, 1):
        raise ValueError(f"unsupported sidx version {version}")
    if timescale <= 0:
        raise ValueError("sidx timescale must be positive")
    if len(references) > MAX_REFERENCES:
        raise ValueError(f"too many sidx references: {len(references)}")
    limit = 0xFFFFFFFF if version == 0 else 0xFFFFFFFFFFFFFFFF
    if earliest_presentation_time > limit or first_offset > limit:
        version = 1
        limit = 0xFFFFFFFFFFFFFFFF
    if earliest_presentation_time > limit or first_offset > limit:
        raise ValueError("sidx timing or offset does not fit the version 1 fields")
    if not 0 <= reference_id <= 0xFFFFFFFF:
        raise ValueError("sidx reference_id does not fit 32 bits")

    body = bytearray()
    body += struct.pack(">B3sII", version, b"\x00\x00\x00", reference_id, timescale)
    if version == 0:
        body += struct.pack(">II", earliest_presentation_time, first_offset)
    else:
        body += struct.pack(">QQ", earliest_presentation_time, first_offset)
    body += struct.pack(">HH", 0, len(references))
    for size, duration, sap_word in references:
        if not 0 < size <= 0x7FFFFFFF:
            raise ValueError(f"referenced_size {size} does not fit 31 bits")
        if not 0 <= duration <= 0xFFFFFFFF:
            raise ValueError(f"subsegment_duration {duration} does not fit 32 bits")
        if not 0 <= sap_word <= 0xFFFFFFFF:
            raise ValueError("sap word does not fit 32 bits")
        body += struct.pack(">III", size, duration, sap_word)
    return struct.pack(">I4s", 8 + len(body), b"sidx") + bytes(body)


def build_references(
    segments: list, timescale: int, *, all_sync: bool = True
) -> list[tuple[int, int, int]]:
    """Convert layout segments into sidx reference tuples in ``timescale``."""

    if timescale <= 0:
        raise ValueError("timescale must be positive")
    references: list[tuple[int, int, int]] = []
    for segment in segments:
        size = segment.byte_end - segment.byte_start
        if size <= 0:
            raise ValueError(f"segment {segment.index} has non-positive byte size")
        if segment.duration % 1:
            # Durations are integral ticks by construction; a fractional value
            # would mean the layout mixed time bases.
            raise ValueError("segment duration is not an integral tick count")
        duration = int(segment.duration)
        sap_word = (1 << 31) | (1 << 28) if all_sync else 1 << 28
        references.append((size, duration, sap_word))
    return references
