"""Parse the individual stbl tables: stts, stsc, stsz, stco and co64.

Each parser validates its own declared entry count against the bytes that are
actually present before allocating anything.
"""

from __future__ import annotations

import struct

from .box_reader import Box, find_all
from .cursor import Cursor
from .errors import StructureError

MAX_SAMPLES = 5_000_000
MAX_TABLE_ENTRIES = 2_000_000
MAX_CHUNKS = 5_000_000


def cursor_for(view: memoryview, box: Box) -> Cursor:
    return Cursor(view, box.payload_start, box.end, box.type, box.start)


def _pre_defined_flags(cursor: Cursor) -> int:
    return (cursor.u8() << 16) | (cursor.u8() << 8) | cursor.u8()


def _u32_table(cursor: Cursor, box: Box, what: str) -> list[int]:
    count = cursor.u32()
    if count > MAX_TABLE_ENTRIES:
        raise StructureError(
            f"{what} entry count {count} exceeds limit {MAX_TABLE_ENTRIES}",
            box_type=box.type,
            offset=box.start,
        )
    # Verify the payload actually holds the declared entries before reading.
    if count * 4 > cursor.remaining:
        raise StructureError(
            f"{what} declares {count} entries but only {cursor.remaining} "
            "payload bytes remain",
            box_type=box.type,
            offset=box.start,
        )
    return [cursor.u32() for _ in range(count)]


# --------------------------------------------------------------------------
# Individual sample table boxes
# --------------------------------------------------------------------------


def parse_stts(view: memoryview, box: Box) -> list[tuple[int, int]]:
    cursor = cursor_for(view, box)
    version, flags = cursor.u8(), _pre_defined_flags(cursor)
    if version != 0 or flags:
        raise StructureError(
            f"stts must use version 0 with zero flags (got version {version}, "
            f"flags 0x{flags:x})",
            box_type=box.type,
            offset=box.start,
        )
    count = cursor.u32()
    if count > MAX_TABLE_ENTRIES:
        raise StructureError(
            f"stts entry count {count} exceeds limit {MAX_TABLE_ENTRIES}",
            box_type=box.type,
            offset=box.start,
        )
    if count * 8 != cursor.remaining:
        raise StructureError(
            f"stts declares {count} entries but has {cursor.remaining} payload bytes",
            box_type=box.type,
            offset=box.start,
        )
    entries: list[tuple[int, int]] = []
    total_samples = 0
    for _ in range(count):
        sample_count = cursor.u32()
        sample_delta = cursor.u32()
        if sample_count > MAX_SAMPLES - total_samples:
            raise StructureError(
                "stts sample count overflows the supported sample limit",
                box_type=box.type,
                offset=box.start,
            )
        total_samples += sample_count
        entries.append((sample_count, sample_delta))
    return entries


def parse_stsc(view: memoryview, box: Box) -> list[tuple[int, int, int]]:
    cursor = cursor_for(view, box)
    if cursor.u8() != 0 or _pre_defined_flags(cursor):
        raise StructureError(
            "stsc must use version 0 with zero flags",
            box_type=box.type,
            offset=box.start,
        )
    count = cursor.u32()
    if count > MAX_TABLE_ENTRIES:
        raise StructureError(
            f"stsc entry count {count} exceeds limit {MAX_TABLE_ENTRIES}",
            box_type=box.type,
            offset=box.start,
        )
    if count * 12 != cursor.remaining:
        raise StructureError(
            f"stsc declares {count} entries but has {cursor.remaining} payload bytes",
            box_type=box.type,
            offset=box.start,
        )
    entries: list[tuple[int, int, int]] = []
    previous_chunk = 0
    for _ in range(count):
        first_chunk = cursor.u32()
        samples_per_chunk = cursor.u32()
        descriptor = cursor.u32()
        if first_chunk == 0:
            raise StructureError(
                "stsc first_chunk is one-based; 0 is invalid",
                box_type=box.type,
                offset=box.start,
            )
        if first_chunk <= previous_chunk:
            raise StructureError(
                f"stsc first_chunk values are not strictly increasing "
                f"({previous_chunk} then {first_chunk})",
                box_type=box.type,
                offset=box.start,
            )
        if samples_per_chunk == 0:
            raise StructureError(
                f"stsc chunk {first_chunk} declares zero samples",
                box_type=box.type,
                offset=box.start,
            )
        previous_chunk = first_chunk
        entries.append((first_chunk, samples_per_chunk, descriptor))
    if entries and entries[0][0] != 1:
        raise StructureError(
            "stsc must start at chunk 1",
            box_type=box.type,
            offset=box.start,
        )
    return entries


def parse_stsz(view: memoryview, box: Box) -> tuple[int, list[int]]:
    cursor = cursor_for(view, box)
    if cursor.u8() != 0 or _pre_defined_flags(cursor):
        raise StructureError(
            "stsz must use version 0 with zero flags",
            box_type=box.type,
            offset=box.start,
        )
    uniform_size = cursor.u32()
    count = cursor.u32()
    if count > MAX_SAMPLES:
        raise StructureError(
            f"stsz sample count {count} exceeds limit {MAX_SAMPLES}",
            box_type=box.type,
            offset=box.start,
        )
    if uniform_size == 0:
        if count * 4 > cursor.remaining:
            raise StructureError(
                f"stsz declares {count} sample sizes but only "
                f"{cursor.remaining} payload bytes remain",
                box_type=box.type,
                offset=box.start,
            )
        sizes = [cursor.u32() for _ in range(count)]
    else:
        if cursor.remaining:
            raise StructureError(
                f"stsz declares a uniform size but carries {cursor.remaining} "
                "extra payload bytes",
                box_type=box.type,
                offset=box.start,
            )
        sizes = [uniform_size] * count
    if count == 0:
        raise StructureError(
            "stsz declares zero samples", box_type=box.type, offset=box.start
        )
    for position, size in enumerate(sizes):
        if size == 0:
            raise StructureError(
                f"sample {position} has a zero byte size",
                box_type=box.type,
                offset=box.start,
            )
    return uniform_size, sizes


def parse_stco(view: memoryview, box: Box) -> list[int]:
    cursor = cursor_for(view, box)
    if cursor.u8() != 0 or _pre_defined_flags(cursor):
        raise StructureError(
            "stco must use version 0 with zero flags",
            box_type=box.type,
            offset=box.start,
        )
    offsets = _u32_table(cursor, box, "stco")
    if cursor.remaining:
        raise StructureError(
            f"stco has {cursor.remaining} unexpected trailing bytes",
            box_type=box.type,
            offset=box.start,
        )
    return offsets


def parse_co64(view: memoryview, box: Box) -> list[int]:
    cursor = cursor_for(view, box)
    if cursor.u8() != 0 or _pre_defined_flags(cursor):
        raise StructureError(
            "co64 must use version 0 with zero flags",
            box_type=box.type,
            offset=box.start,
        )
    count = cursor.u32()
    if count > MAX_CHUNKS:
        raise StructureError(
            f"co64 chunk count {count} exceeds limit {MAX_CHUNKS}",
            box_type=box.type,
            offset=box.start,
        )
    if count * 8 > cursor.remaining:
        raise StructureError(
            f"co64 declares {count} offsets but only {cursor.remaining} "
            "payload bytes remain",
            box_type=box.type,
            offset=box.start,
        )
    return [cursor.u64() for _ in range(count)]
