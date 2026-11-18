"""stbl sample-table parsing: stsz/stz2/stts/ctts/stsc/stco/co64/stss.

Each parser validates its own truncation and entry-count limits before any
allocation, records the absolute source offset of every structure, and
treats a missing optional table (ctts, stss) as valid. Cross-table
consistency lives in :func:`parse_stbl` / :mod:`media_index.rebuild`.
"""

from __future__ import annotations

from . import limits
from .boxes import (
    FULLBOX_HEADER,
    BoxHeader,
    fullbox_version,
    i32,
    iter_boxes,
    u16,
    u32,
    u64,
)
from .errors import LimitError, ParseError
from .model import (
    ChunkOffsetBox,
    CttsBox,
    CttsEntry,
    StblTables,
    StscBox,
    StscEntry,
    StssBox,
    StszBox,
    SttsBox,
    SttsEntry,
    Stz2Box,
)


def _check_count(count: int, limit: int, what: str, offset: int) -> None:
    if count < 0:
        raise ParseError(f"{what} entry count is negative", offset)
    if count > limit:
        raise LimitError(f"{what} declares {count} entries, limit {limit}", offset)


def parse_stsz(header: BoxHeader, payload: memoryview) -> StszBox:
    if len(payload) < FULLBOX_HEADER + 8:
        raise ParseError("truncated stsz header fields", header.header_start)
    uniform = u32(payload, FULLBOX_HEADER)
    count = u32(payload, FULLBOX_HEADER + 4)
    _check_count(count, limits.MAX_TABLE_ENTRIES, "stsz", header.header_start)
    sizes: list[int] = []
    if uniform == 0:
        if len(payload) < FULLBOX_HEADER + 8 + count * 4:
            raise ParseError("truncated stsz sample size entries", header.header_start)
        rel = FULLBOX_HEADER + 8
        for i in range(count):
            sizes.append(u32(payload, rel + i * 4))
    elif len(payload) != FULLBOX_HEADER + 8:
        raise ParseError(
            "uniform stsz must not carry per-sample size entries", header.header_start
        )
    return StszBox(
        source_offset=header.header_start,
        uniform_sample_size=uniform,
        sample_count=count,
        entry_sizes=tuple(sizes),
    )


def parse_stz2(header: BoxHeader, payload: memoryview) -> Stz2Box:
    if len(payload) < FULLBOX_HEADER + 8:
        raise ParseError("truncated stz2 header fields", header.header_start)
    # ISO layout: reserved(u24) then field_size(u8), then sample_count(u32).
    field_size = payload[FULLBOX_HEADER + 3]
    if field_size not in (4, 8, 16):
        raise ParseError(
            f"stz2 field_size {field_size} is not one of 4/8/16", header.header_start
        )
    count = u32(payload, FULLBOX_HEADER + 4)
    _check_count(count, limits.MAX_TABLE_ENTRIES, "stz2", header.header_start)
    packed_bytes = (
        (count + 1) // 2 if field_size == 4 else count * (field_size // 8)
    )
    if len(payload) < FULLBOX_HEADER + 8 + packed_bytes:
        raise ParseError("truncated stz2 packed sample sizes", header.header_start)
    sizes: list[int] = []
    rel = FULLBOX_HEADER + 8
    if field_size == 4:
        for i in range(count):
            byte = payload[rel + i // 2]
            nibble = (byte >> 4) if i % 2 == 0 else (byte & 0x0F)
            sizes.append(nibble)
    elif field_size == 8:
        sizes.extend(payload[rel : rel + count])
    else:  # 16-bit
        sizes.extend(u16(payload, rel + i * 2) for i in range(count))
    return Stz2Box(
        source_offset=header.header_start,
        field_size=field_size,
        sample_count=count,
        entry_sizes=tuple(sizes),
    )


def parse_stts(header: BoxHeader, payload: memoryview) -> SttsBox:
    if len(payload) < FULLBOX_HEADER + 4:
        raise ParseError("truncated stts entry count", header.header_start)
    entry_count = u32(payload, FULLBOX_HEADER)
    _check_count(entry_count, limits.MAX_TABLE_ENTRIES, "stts", header.header_start)
    if len(payload) < FULLBOX_HEADER + 4 + entry_count * 8:
        raise ParseError("truncated stts entries", header.header_start)
    entries: list[SttsEntry] = []
    total_samples = 0
    total_ticks = 0
    rel = FULLBOX_HEADER + 4
    for i in range(entry_count):
        src = header.header_start + rel + i * 8
        count = u32(payload, rel + i * 8)
        delta = u32(payload, rel + i * 8 + 4)
        if count == 0:
            raise ParseError("stts entry with zero sample_count", src)
        if delta == 0:
            raise ParseError("stts entry with zero sample_delta", src)
        total_samples += count
        total_ticks = _accumulate(total_ticks, count, delta, src)
        entries.append(SttsEntry(count, delta, src))
    if total_samples > limits.MAX_TABLE_ENTRIES:
        raise LimitError("stts expanded sample count exceeds limit", header.header_start)
    return SttsBox(header.header_start, tuple(entries), total_samples, total_ticks)

def parse_ctts(header: BoxHeader, payload: memoryview) -> CttsBox:
    version = fullbox_version(payload)
    if version not in (0, 1):
        raise ParseError(f"unsupported ctts version {version}", header.header_start)
    if len(payload) < FULLBOX_HEADER + 4:
        raise ParseError("truncated ctts entry count", header.header_start)
    entry_count = u32(payload, FULLBOX_HEADER)
    _check_count(entry_count, limits.MAX_TABLE_ENTRIES, "ctts", header.header_start)
    if len(payload) < FULLBOX_HEADER + 4 + entry_count * 8:
        raise ParseError("truncated ctts entries", header.header_start)
    entries: list[CttsEntry] = []
    total = 0
    rel = FULLBOX_HEADER + 4
    for i in range(entry_count):
        src = header.header_start + rel + i * 8
        count = u32(payload, rel + i * 8)
        if count == 0:
            raise ParseError("ctts entry with zero sample_count", src)
        offset = i32(payload, rel + i * 8 + 4) if version == 1 else u32(
            payload, rel + i * 8 + 4
        )
        total += count
        entries.append(CttsEntry(count, offset, src))
    if total > limits.MAX_TABLE_ENTRIES:
        raise LimitError("ctts expanded sample count exceeds limit", header.header_start)
    return CttsBox(header.header_start, version, tuple(entries), total)


def parse_stsc(header: BoxHeader, payload: memoryview) -> StscBox:
    if len(payload) < FULLBOX_HEADER + 4:
        raise ParseError("truncated stsc entry count", header.header_start)
    entry_count = u32(payload, FULLBOX_HEADER)
    _check_count(entry_count, limits.MAX_TABLE_ENTRIES, "stsc", header.header_start)
    if len(payload) < FULLBOX_HEADER + 4 + entry_count * 12:
        raise ParseError("truncated stsc entries", header.header_start)
    entries: list[StscEntry] = []
    rel = FULLBOX_HEADER + 4
    prev_first = 0
    for i in range(entry_count):
        src = header.header_start + rel + i * 12
        first_chunk = u32(payload, rel + i * 12)
        spc = u32(payload, rel + i * 12 + 4)
        desc = u32(payload, rel + i * 12 + 8)
        if first_chunk <= prev_first or spc == 0:
            raise ParseError(
                "stsc first_chunk must strictly increase and samples_per_chunk be > 0",
                src,
            )
        prev_first = first_chunk
        entries.append(StscEntry(first_chunk, spc, desc, src))
    return StscBox(header.header_start, tuple(entries))


def parse_chunk_offsets(header: BoxHeader, payload: memoryview) -> ChunkOffsetBox:
    is_co64 = header.type == "co64"
    if len(payload) < FULLBOX_HEADER + 4:
        raise ParseError(f"truncated {header.type} entry count", header.header_start)
    count = u32(payload, FULLBOX_HEADER)
    _check_count(count, limits.MAX_CHUNKS, header.type, header.header_start)
    width = 8 if is_co64 else 4
    if len(payload) < FULLBOX_HEADER + 4 + count * width:
        raise ParseError(f"truncated {header.type} offsets", header.header_start)
    rel = FULLBOX_HEADER + 4
    reader = (lambda i: u64(payload, rel + i * 8)) if is_co64 else (
        lambda i: u32(payload, rel + i * 4)
    )
    return ChunkOffsetBox(header.header_start, header.type, tuple(
        reader(i) for i in range(count)
    ))


def parse_stss(header: BoxHeader, payload: memoryview) -> StssBox:
    if len(payload) < FULLBOX_HEADER + 4:
        raise ParseError("truncated stss entry count", header.header_start)
    count = u32(payload, FULLBOX_HEADER)
    _check_count(count, limits.MAX_TABLE_ENTRIES, "stss", header.header_start)
    if len(payload) < FULLBOX_HEADER + 4 + count * 4:
        raise ParseError("truncated stss entries", header.header_start)
    rel = FULLBOX_HEADER + 4
    sync = tuple(u32(payload, rel + i * 4) for i in range(count))
    if any(s <= 0 for s in sync):
        raise ParseError("stss sample numbers must be 1-based positive", header.header_start)
    return StssBox(header.header_start, sync)


def _accumulate(total: int, count: int, delta: int, src: int) -> int:
    if count > limits.MAX_TICK_ACCUM // max(delta, 1):
        raise ParseError("stts duration accumulation overflow", src)
    step = count * delta
    if total > limits.MAX_TICK_ACCUM - step:
        raise ParseError("stts duration accumulation overflow", src)
    return total + step
