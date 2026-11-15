"""Parsing and cross-checking of the ``stbl`` sample table leaf boxes.

Covered boxes: ``stsz`` (sample sizes), ``stsc`` (sample-to-chunk),
``stco``/``co64`` (32/64-bit chunk offsets) and ``stts`` (decode timing).
The offset and timing parsers live in :mod:`sidxcheck.tables`.
"""

from __future__ import annotations

import struct

from . import model
from .boxes import LocatedBox, read_fullbox_prefix
from .tables import MAX_TABLE_ENTRIES, need, parse_offsets, parse_stts


def parse_stsz(located: LocatedBox, scan) -> tuple[list[int], int, int] | None:
    """Return (sample_sizes, sample_count_field, uniform_sample_size)."""
    header, payload = located.header, located.payload
    prefix = read_fullbox_prefix(payload, header, scan)
    if prefix is None:
        return None
    _, _, pos = prefix
    if not need(len(payload), pos, 8, located, scan, "stsz fixed fields"):
        return None
    uniform, count = struct.unpack_from(">II", payload, pos)
    pos += 8
    if count > MAX_TABLE_ENTRIES:
        scan.fail(
            model.ENTRY_COUNT_UNTRUSTED,
            f"stsz at offset {header.box_offset} sample_count {count} exceeds "
            f"the trusted limit {MAX_TABLE_ENTRIES}",
            box_offset=header.box_offset,
            box_type="stsz",
        )
        return None
    sizes: list[int] = []
    if uniform == 0:
        if pos + count * 4 > len(payload):
            scan.fail(
                model.ENTRY_COUNT_UNTRUSTED,
                f"stsz at offset {header.box_offset} promises {count} size "
                "entries that do not fit in the payload",
                box_offset=header.box_offset,
                box_type="stsz",
            )
            return None
        sizes = list(struct.unpack_from(f">{count}I", payload, pos))
    return sizes, count, uniform


def parse_stsc(located: LocatedBox, scan) -> list[model.StscEntry] | None:
    header, payload = located.header, located.payload
    prefix = read_fullbox_prefix(payload, header, scan)
    if prefix is None:
        return None
    _, _, pos = prefix
    if not need(len(payload), pos, 4, located, scan, "stsc entry_count"):
        return None
    (count,) = struct.unpack_from(">I", payload, pos)
    pos += 4
    if count > MAX_TABLE_ENTRIES or pos + count * 12 > len(payload):
        scan.fail(
            model.ENTRY_COUNT_UNTRUSTED,
            f"stsc at offset {header.box_offset} entry_count {count} cannot "
            "fit in the payload",
            box_offset=header.box_offset,
            box_type="stsc",
        )
        return None
    entries: list[model.StscEntry] = []
    for i in range(count):
        first_chunk, samples_per_chunk, desc_index = struct.unpack_from(
            ">III", payload, pos
        )
        pos += 12
        if samples_per_chunk == 0 or desc_index == 0:
            scan.fail(
                model.STSC_SAMPLE_DESC_ZERO,
                f"stsc entry {i} has samples_per_chunk={samples_per_chunk} "
                f"and sample_description_index={desc_index}; both must be >= 1",
                box_offset=header.box_offset,
                box_type="stsc",
                entry_index=i,
            )
            return None
        entries.append(model.StscEntry(first_chunk, samples_per_chunk, desc_index))
    return entries


def build_sample_table(leaves: dict[str, LocatedBox], scan) -> model.SampleTable | None:
    """Assemble and validate all stbl leaf tables for one track."""
    def missing(code: str, name: str, box_offset: int | None = None) -> None:
        scan.fail(
            code,
            f"required sample table box {name!r} is missing",
            box_offset=box_offset,
            box_type=name,
        )

    if "stsz" not in leaves:
        missing(model.STSZ_MISSING, "stsz")
        return None
    parsed_sizes = parse_stsz(leaves["stsz"], scan)
    if parsed_sizes is None:
        return None
    sizes, size_count, uniform = parsed_sizes

    if "stsc" not in leaves:
        missing(model.STSC_MISSING, "stsc")
        return None
    stsc = parse_stsc(leaves["stsc"], scan)
    if stsc is None:
        return None
    if not stsc:
        scan.fail(
            model.STSC_EMPTY,
            "stsc contains zero entries; samples cannot be mapped to chunks",
            box_offset=leaves["stsc"].header.box_offset,
            box_type="stsc",
        )
        return None
    if stsc[0].first_chunk != 1:
        scan.fail(
            model.STSC_FIRST_NOT_ONE,
            f"first stsc entry starts at chunk {stsc[0].first_chunk}; the "
            "first chunk of a track must be chunk 1",
            box_offset=leaves["stsc"].header.box_offset,
            box_type="stsc",
        )
        return None
    for prev, cur in zip(stsc, stsc[1:]):
        if cur.first_chunk <= prev.first_chunk:
            scan.fail(
                model.STSC_NON_MONOTONIC,
                "stsc first_chunk values must be strictly increasing; "
                f"{prev.first_chunk} followed by {cur.first_chunk}",
                box_offset=leaves["stsc"].header.box_offset,
                box_type="stsc",
            )
            return None

    offset_boxes = [name for name in ("stco", "co64") if name in leaves]
    if not offset_boxes:
        missing(model.STCO_MISSING, "stco/co64")
        return None
    chosen_offset = "co64" if "co64" in leaves else "stco"
    if len(offset_boxes) > 1:
        scan.fail(
            model.STCO_DUPLICATE_VARIANT,
            "both stco and co64 present; using co64 and ignoring stco",
            box_offset=leaves[chosen_offset].header.box_offset,
            box_type=chosen_offset,
            severity=model.SEV_WARNING,
        )
    parsed_offsets = parse_offsets(leaves[chosen_offset], scan)
    if parsed_offsets is None:
        return None
    chunk_offsets, mode = parsed_offsets

    if "stts" not in leaves:
        missing(model.STTS_MISSING, "stts")
        return None
    stts = parse_stts(leaves["stts"], scan)
    if stts is None:
        return None

    # stsc run lengths expand into a samples-per-chunk value per chunk.
    chunk_count = len(chunk_offsets)
    per_chunk: list[int] = []
    run = 0
    for chunk_index in range(1, chunk_count + 1):
        while run + 1 < len(stsc) and stsc[run + 1].first_chunk <= chunk_index:
            run += 1
        if stsc[run].first_chunk > chunk_index:
            break
        per_chunk.append(stsc[run].samples_per_chunk)

    mapped_total = sum(per_chunk)
    timing_total = sum(e.sample_count for e in stts)

    effective_count = size_count
    if size_count != mapped_total or size_count != timing_total:
        scan.fail(
            model.SAMPLE_COUNT_INCONSISTENT,
            "sample counts disagree: stsz="
            f"{size_count}, stsc-expanded={mapped_total}, stts={timing_total}",
            box_offset=leaves["stsz"].header.box_offset,
            box_type="stbl",
        )
        # Continue only with the portion every table can describe.
        effective_count = min(size_count, mapped_total, timing_total)
        if effective_count == 0:
            return None

    # Lay samples out consecutively inside each chunk per the stsc mapping.
    sample_offsets: list[int] = []
    sample_cursor = 0
    for chunk_index, samples_in_chunk in enumerate(per_chunk):
        if sample_cursor >= effective_count:
            break
        offset = chunk_offsets[chunk_index]
        for _ in range(samples_in_chunk):
            if sample_cursor >= effective_count:
                break
            sample_offsets.append(offset)
            size = uniform if uniform else sizes[sample_cursor]
            offset += size
            sample_cursor += 1

    if sample_cursor != effective_count:
        scan.fail(
            model.SAMPLE_TABLE_INCOMPLETE,
            f"only {sample_cursor} of {effective_count} samples could be "
            "located through the chunk mapping",
            box_offset=leaves["stsc"].header.box_offset,
            box_type="stbl",
        )

    return model.SampleTable(
        sample_sizes=sizes,
        sample_count_field=size_count,
        uniform_sample_size=uniform,
        stsc_entries=stsc,
        chunk_offsets=chunk_offsets,
        offset_mode=mode,
        stts_entries=stts,
        total_samples=sample_cursor,
        sample_offsets=sample_offsets,
    )


def sample_size_at(table: model.SampleTable, index: int) -> int:
    """Return the byte size of sample ``index`` (0-based)."""
    if table.uniform_sample_size:
        return table.uniform_sample_size
    return table.sample_sizes[index]
