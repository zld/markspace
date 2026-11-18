"""stbl discovery, table assembly and sample/chunk mapping helpers.

The parsers in :mod:`media_index.stbl` validate each table in isolation.
This module walks the moov/trak/mdia/minf/stbl container chain, assembles a
:class:`StblTables`, expands the run-length stsc map into per-chunk sample
counts and turns sample sizes plus chunk offsets into concrete byte spans.
"""

from __future__ import annotations

from dataclasses import dataclass

from .boxes import BoxHeader, find_first, find_all, iter_boxes
from .errors import ParseError
from .model import StblTables


# Container chain leading from a trak to its stbl.
_CONTAINER_CHAIN = ("mdia", "minf", "stbl")


def find_stbl_views(view: memoryview) -> list[tuple[BoxHeader, memoryview]]:
    """Return every stbl box found under moov/trak chains in ``view``."""

    moov = find_first(view, "moov")
    if moov is None:
        return []
    _, moov_payload = moov
    out: list[tuple[BoxHeader, memoryview]] = []
    for _, trak_payload in find_all(moov_payload, "trak"):
        current = trak_payload
        header = None
        for level in _CONTAINER_CHAIN:
            found = find_first(current, level, base=_view_base(header))
            if found is None:
                header = None
                break
            header, current = found
        if header is not None:
            out.append((header, current))
    return out


def _view_base(parent_header: BoxHeader | None) -> int:
    """Absolute base of a container payload, or 0 for a top-level view."""

    return parent_header.content_start if parent_header is not None else 0


def parse_stbl(header: BoxHeader, payload: memoryview) -> StblTables:
    """Assemble one stbl payload into a validated :class:`StblTables`."""

    from . import stbl as stbl_mod

    boxes = {
        h.type: (h, p)
        for h, p in iter_boxes(payload, base=header.content_start)
    }
    if "stsz" in boxes and "stz2" in boxes:
        raise ParseError(
            "stbl contains both stsz and stz2 sample size tables", header.header_start
        )
    size_box = boxes.get("stsz") or boxes.get("stz2")
    if size_box is None:
        raise ParseError("stbl missing sample size table (stsz/stz2)", header.header_start)
    for required in ("stts", "stsc"):
        if required not in boxes:
            raise ParseError(f"stbl missing required box {required}", header.header_start)
    if "stco" not in boxes and "co64" not in boxes:
        raise ParseError("stbl missing chunk offset table (stco/co64)", header.header_start)

    size_table = (
        stbl_mod.parse_stsz(*boxes["stsz"])
        if "stsz" in boxes
        else stbl_mod.parse_stz2(*boxes["stz2"])
    )
    stts = stbl_mod.parse_stts(*boxes["stts"])
    stsc = stbl_mod.parse_stsc(*boxes["stsc"])
    co_key = "stco" if "stco" in boxes else "co64"
    chunk_offsets = stbl_mod.parse_chunk_offsets(*boxes[co_key])
    ctts = stbl_mod.parse_ctts(*boxes["ctts"]) if "ctts" in boxes else None
    stss = stbl_mod.parse_stss(*boxes["stss"]) if "stss" in boxes else None

    return StblTables(
        stbl_offset=header.header_start,
        size_table=size_table,
        stts=stts,
        stsc=stsc,
        chunk_offsets=chunk_offsets,
        ctts=ctts,
        stss=stss,
    )


@dataclass(frozen=True)
class ChunkSpan:
    """A chunk's absolute byte range and its 1-based sample index span."""

    chunk_index: int  # 0-based
    offset: int
    length: int
    first_sample: int  # 1-based, inclusive
    last_sample: int


def samples_per_chunk(stsc_entries, chunk_count: int) -> tuple[int, ...]:
    """Expand stsc run entries into one samples-per-chunk tuple."""

    if chunk_count == 0:
        if stsc_entries:
            raise ParseError("stsc has entries but chunk offset table is empty")
        return ()
    if not stsc_entries:
        raise ParseError("chunks exist but stsc has no entries")
    if stsc_entries[0].first_chunk != 1:
        raise ParseError("stsc first entry must start at first_chunk=1")
    result: list[int] = []
    entry_idx = 0
    for chunk in range(1, chunk_count + 1):
        while (
            entry_idx + 1 < len(stsc_entries)
            and stsc_entries[entry_idx + 1].first_chunk <= chunk
        ):
            entry_idx += 1
        entry = stsc_entries[entry_idx]
        if entry.first_chunk > chunk:
            raise ParseError(
                f"stsc has no coverage for chunk {chunk}", entry.source_offset
            )
        result.append(entry.samples_per_chunk)
    last = stsc_entries[-1]
    if last.first_chunk > chunk_count:
        raise ParseError(
            f"stsc first_chunk {last.first_chunk} beyond chunk count {chunk_count}",
            last.source_offset,
        )
    return tuple(result)


def build_chunk_spans(tables: StblTables) -> tuple[ChunkSpan, ...]:
    """Resolve every chunk to an absolute byte range and sample span."""

    per_chunk = samples_per_chunk(tables.stsc.entries, tables.chunk_count)
    sizes = _sample_sizes(tables)
    offsets = tables.chunk_offsets.offsets
    spans: list[ChunkSpan] = []
    sample_cursor = 1
    size_cursor = 0
    for chunk_index, count in enumerate(per_chunk):
        end_cursor = size_cursor + count
        if end_cursor > len(sizes):
            raise ParseError(
                f"chunk {chunk_index + 1} requests samples beyond size table",
                tables.stsc.source_offset,
            )
        length = sum(sizes[size_cursor:end_cursor])
        spans.append(
            ChunkSpan(
                chunk_index=chunk_index,
                offset=offsets[chunk_index],
                length=length,
                first_sample=sample_cursor,
                last_sample=sample_cursor + count - 1,
            )
        )
        sample_cursor += count
        size_cursor = end_cursor
    if size_cursor != tables.sample_count:
        raise ParseError(
            f"stsc covers {size_cursor} samples but size table declares "
            f"{tables.sample_count}",
            tables.stsc.source_offset,
        )
    return tuple(spans)


def _sample_sizes(tables: StblTables) -> tuple[int, ...]:
    table = tables.size_table
    if hasattr(table, "uniform_sample_size") and table.uniform_sample_size:
        return tuple([table.uniform_sample_size] * table.sample_count)
    return table.entry_sizes
