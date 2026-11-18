"""sidx (Segment Index Box) parsing for versions 0 and 1.

Version 0 uses 32-bit times and first_offset; version 1 uses 64-bit values.
Reference byte ranges are resolved to absolute file offsets here, and index
references are followed recursively with a hard depth limit and cycle
detection. Everything reads through :mod:`media_index.boxes` bounded views.
"""

from __future__ import annotations

from . import limits
from .boxes import (
    FULLBOX_HEADER,
    BoxHeader,
    bounded_view,
    find_first,
    fullbox_version,
    iter_boxes,
    u16,
    u32,
    u64,
)
from .errors import LimitError, NestedIndexError, ParseError
from .model import REF_INDEX, REF_MEDIA, SidxBox, SidxReference, SidxTree

# Fixed layout after the version/flags FullBox header.
# reference_id(u32) timescale(u32) time(u32/64) offset(u32/64) reserved(u16) count(u16)
_ROOT_FIELDS_V0 = 4 + 4 + 4 + 4 + 2 + 2
_ROOT_FIELDS_V1 = 4 + 4 + 8 + 8 + 2 + 2
ENTRY_SIZE = 12


def parse_sidx(header: BoxHeader, payload: memoryview) -> SidxBox:
    """Parse one sidx box payload into a :class:`SidxBox`."""

    version = fullbox_version(payload)
    if version not in (0, 1):
        raise ParseError(f"unsupported sidx version {version}", header.header_start)
    rel = FULLBOX_HEADER
    reference_id = u32(payload, rel)
    timescale = u32(payload, rel + 4)
    if timescale == 0:
        raise ParseError("sidx timescale must be non-zero", header.header_start + rel + 4)
    if version == 0:
        # 32-bit earliest_presentation_time then 32-bit first_offset.
        ept = u32(payload, rel + 8)
        first_offset = u32(payload, rel + 12)
        count_rel = rel + 16
        need = _ROOT_FIELDS_V0
    else:
        # 64-bit earliest_presentation_time then 64-bit first_offset.
        ept = u64(payload, rel + 8)
        first_offset = u64(payload, rel + 16)
        count_rel = rel + 24
        need = _ROOT_FIELDS_V1
    if len(payload) < FULLBOX_HEADER + need:
        raise ParseError("truncated sidx root fields", header.header_start)
    entry_count = u16(payload, count_rel + 2)
    if entry_count > limits.MAX_SIDX_REFERENCES:
        raise LimitError(
            f"sidx declares {entry_count} references, limit {limits.MAX_SIDX_REFERENCES}",
            header.header_start,
        )
    expected = FULLBOX_HEADER + need + entry_count * ENTRY_SIZE
    if len(payload) < expected:
        raise ParseError(
            f"truncated sidx entries: need {expected} payload bytes, have {len(payload)}",
            header.header_start,
        )

    # The anchor for the first reference is the first byte after this box.
    anchor = header.end
    refs: list[SidxReference] = []
    cursor = anchor + first_offset
    entry_rel = FULLBOX_HEADER + need
    for i in range(entry_count):
        src = header.header_start + entry_rel + i * ENTRY_SIZE
        first_word = u32(payload, entry_rel + i * ENTRY_SIZE)
        ref_type = (first_word >> 31) & 1
        ref_size = first_word & 0x7FFFFFFF
        duration = u32(payload, entry_rel + i * ENTRY_SIZE + 4)
        sap_word = u32(payload, entry_rel + i * ENTRY_SIZE + 8)
        starts_sap = bool((sap_word >> 31) & 1)
        sap_type = (sap_word >> 28) & 0x7
        sap_delta = sap_word & 0x0FFFFFFF
        if ref_type not in (REF_MEDIA, REF_INDEX):
            raise ParseError(f"unknown sidx reference type {ref_type}", src)
        if ref_size == 0 and ref_type == REF_MEDIA:
            raise ParseError("media reference with zero referenced_size", src)
        start = cursor
        end = start + ref_size  # ref_size < 2^31, cannot overflow with checked anchor
        if end > (1 << 63):
            raise ParseError("sidx reference byte range overflows 63-bit space", src)
        refs.append(
            SidxReference(
                order=i + 1,
                reference_type=ref_type,
                referenced_size=ref_size,
                subsegment_duration=duration,
                starts_with_sap=starts_sap,
                sap_type=sap_type,
                sap_delta_time=sap_delta,
                source_offset=src,
                byte_start=start,
                byte_end=end,
            )
        )
        cursor = end  # contiguous references share the boundary
    return SidxBox(
        box_start=header.header_start,
        source_offset=header.content_start,
        version=version,
        reference_id=reference_id,
        timescale=timescale,
        earliest_presentation_time=ept,
        first_offset=first_offset,
        references=tuple(refs),
    )


def _locate_sidx(view: memoryview, base: int, position: int) -> tuple[BoxHeader, memoryview]:
    """Find an sidx box whose header starts at absolute ``position``."""

    found = find_first(bounded_view(view, base, position, base + len(view)),
                       "sidx", base=position)
    if found is None:
        raise NestedIndexError(f"no sidx box at expected position {position}", position)
    return found


def resolve_sidx(
    view: memoryview, base: int, header: BoxHeader, payload: memoryview
) -> SidxTree:
    """Resolve a top-level sidx, following index references recursively."""

    root = parse_sidx(header, payload)
    return _resolve(view, base, root, frozenset())


def _resolve(
    view: memoryview, base: int, box: SidxBox, visited: frozenset[int]
) -> SidxTree:
    if box.box_start in visited:
        raise NestedIndexError(
            "cyclic sidx reference detected", box.box_start
        )
    if len(visited) >= limits.MAX_SIDX_NESTING:
        raise LimitError(
            f"sidx nesting exceeds {limits.MAX_SIDX_NESTING} levels", box.box_start
        )
    next_visited = visited | {box.box_start}
    children: list[SidxTree] = []
    for ref in box.references:
        if ref.reference_type != REF_INDEX:
            continue
        child_header, child_payload = _locate_sidx(view, base, ref.byte_start)
        # Nested sidx must fit entirely inside the referencing byte range.
        if child_header.end > ref.byte_end:
            raise NestedIndexError(
                f"nested sidx at {child_header.header_start} exceeds referencing range "
                f"ending at {ref.byte_end}",
                child_header.header_start,
            )
        child_box = parse_sidx(child_header, child_payload)
        children.append(_resolve(view, base, child_box, next_visited))
    return SidxTree(box=box, children=tuple(children))


def find_top_level_sidx(view: memoryview, base: int = 0) -> tuple[BoxHeader, memoryview] | None:
    """Return the first top-level sidx box in a file view."""

    return find_first(view, "sidx", base=base)


def all_sidx_boxes(view: memoryview, base: int = 0) -> list[tuple[BoxHeader, memoryview]]:
    """List every top-level sidx box (used to detect unexpected duplicates)."""

    return [
        (h, p) for h, p in iter_boxes(view, base=base) if h.type == "sidx"
    ]
