"""Leaf-table parsers for chunk offsets (stco/co64) and decode timing (stts).

Kept separate from :mod:`sidxcheck.stbl` so each source module stays small;
both parsers share the same truncation and untrusted-count classification as
the rest of the sample-table code.
"""

from __future__ import annotations

import struct

from . import model
from .boxes import LocatedBox, read_fullbox_prefix

# Entry counts above this cannot plausibly belong to an offline media track
# and would only be used to drive oversized allocations.
MAX_TABLE_ENTRIES = 1 << 28


def need(payload_len: int, pos: int, width: int, header: LocatedBox,
         scan, what: str) -> bool:
    """Validate that ``width`` more bytes are available in a leaf payload."""
    if pos + width <= payload_len:
        return True
    scan.fail(
        model.TABLE_TRUNCATED,
        f"{header.header.box_type} at offset {header.header.box_offset}: "
        f"truncated while reading {what}",
        box_offset=header.header.box_offset,
        box_type=header.header.box_type,
    )
    return False


def parse_offsets(located: LocatedBox, scan) -> tuple[list[int], str] | None:
    """Parse stco (32-bit) or co64 (64-bit) chunk offsets."""
    header, payload = located.header, located.payload
    mode = "co64" if header.box_type == "co64" else "stco"
    width = 8 if mode == "co64" else 4
    prefix = read_fullbox_prefix(payload, header, scan)
    if prefix is None:
        return None
    _, _, pos = prefix
    if not need(len(payload), pos, 4, located, scan, f"{mode} entry_count"):
        return None
    (count,) = struct.unpack_from(">I", payload, pos)
    pos += 4
    if count > MAX_TABLE_ENTRIES or pos + count * width > len(payload):
        scan.fail(
            model.ENTRY_COUNT_UNTRUSTED,
            f"{mode} at offset {header.box_offset} entry_count {count} cannot "
            "fit in the payload",
            box_offset=header.box_offset,
            box_type=header.box_type,
        )
        return None
    fmt = ">Q" if mode == "co64" else ">I"
    return [
        struct.unpack_from(fmt, payload, pos + i * width)[0]
        for i in range(count)
    ], mode


def parse_stts(located: LocatedBox, scan) -> list[model.SttsEntry] | None:
    header, payload = located.header, located.payload
    prefix = read_fullbox_prefix(payload, header, scan)
    if prefix is None:
        return None
    _, _, pos = prefix
    if not need(len(payload), pos, 4, located, scan, "stts entry_count"):
        return None
    (count,) = struct.unpack_from(">I", payload, pos)
    pos += 4
    if count > MAX_TABLE_ENTRIES or pos + count * 8 > len(payload):
        scan.fail(
            model.ENTRY_COUNT_UNTRUSTED,
            f"stts at offset {header.box_offset} entry_count {count} cannot "
            "fit in the payload",
            box_offset=header.box_offset,
            box_type="stts",
        )
        return None
    entries: list[model.SttsEntry] = []
    for i in range(count):
        sample_count, sample_delta = struct.unpack_from(">II", payload, pos)
        pos += 8
        if sample_count == 0:
            scan.fail(
                model.TABLE_TRUNCATED,
                f"stts entry {i} has zero sample_count",
                box_offset=header.box_offset,
                box_type="stts",
                entry_index=i,
            )
            return None
        entries.append(model.SttsEntry(sample_count, sample_delta))
    return entries
