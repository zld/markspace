"""Parse declared sidx boxes and rebuild references from sample tables."""

from __future__ import annotations

import struct

from .boxes import Box, iter_boxes, read_fullbox
from .model import (
    Boundary,
    MdatExtent,
    SidxIndex,
    SidxReference,
    StructureError,
)


def parse_sidx(root_boxes: tuple[Box, ...], mdats: list[MdatExtent]) -> SidxIndex | None:
    sidx_boxes = iter_boxes(root_boxes, b"sidx")
    if not sidx_boxes:
        return None
    box = sidx_boxes[0]
    version, _flags = read_fullbox(box)
    p = box.payload
    pos = 4
    if version == 0:
        if len(p) < pos + 12:
            raise StructureError("truncated sidx (version 0)")
        reference_id, timescale = struct.unpack_from(">II", p, pos)
        earliest = struct.unpack_from(">I", p, pos + 8)[0]
        first_offset = struct.unpack_from(">I", p, pos + 12)[0]
        pos += 16
    elif version == 1:
        if len(p) < pos + 20:
            raise StructureError("truncated sidx (version 1)")
        reference_id, timescale = struct.unpack_from(">II", p, pos)
        earliest, first_offset = struct.unpack_from(">QQ", p, pos + 8)
        pos += 24
    else:
        raise StructureError(f"unsupported sidx version {version}")
    if len(p) < pos + 8:
        raise StructureError("truncated sidx tail")
    reserved = struct.unpack_from(">H", p, pos)[0]
    if reserved != 0:
        raise StructureError("sidx reserved field is non-zero")
    ref_count = struct.unpack_from(">H", p, pos + 2)[0]
    pos += 4
    if len(p) < pos + 12 * ref_count:
        raise StructureError("truncated sidx reference table")
    # ISO 14496-12: first_offset is relative to the position immediately
    # following the sidx box that contains the reference.
    anchor = box.payload_end
    base_offset = anchor + first_offset
    references: list[SidxReference] = []
    running = base_offset
    for i in range(ref_count):
        raw1, subsegment_duration, raw3 = struct.unpack_from(">III", p, pos + 12 * i)
        referenced_type = raw1 >> 31
        referenced_size = raw1 & 0x7FFFFFFF
        starts_with_sap = bool(raw3 >> 31)
        sap_type = (raw3 >> 28) & 0x7
        sap_delta = raw3 & 0x0FFFFFFF
        if referenced_size == 0:
            raise StructureError("sidx reference with zero size")
        if referenced_type == 1:
            raise StructureError("hierarchical sidx references are not supported")
        offset = running
        end = offset + referenced_size
        if end < offset:
            raise StructureError("sidx reference offset overflows 64-bit range")
        if not _inside_any_mdat(offset, referenced_size, mdats):
            raise StructureError(
                f"sidx reference {i} [{offset},{end}) falls outside mdat"
            )
        references.append(
            SidxReference(
                referenced_type=referenced_type,
                referenced_size=referenced_size,
                subsegment_duration=subsegment_duration,
                starts_with_sap=starts_with_sap,
                sap_type=sap_type,
                sap_delta=sap_delta,
                offset=offset,
            )
        )
        running = end
    return SidxIndex(
        timescale=timescale,
        earliest_presentation_time=earliest,
        first_offset=first_offset,
        reference_id=reference_id,
        references=tuple(references),
        anchor_offset=anchor,
    )


def _inside_any_mdat(offset: int, size: int, mdats: list[MdatExtent]) -> bool:
    end = offset + size
    return any(offset >= m.offset and end <= m.end for m in mdats)


def rebuild_references(boundaries: list[Boundary]) -> tuple[SidxReference, ...]:
    """Turn reconstructed boundaries into the sidx references they imply."""
    refs: list[SidxReference] = []
    for boundary in boundaries:
        refs.append(
            SidxReference(
                referenced_type=0,
                referenced_size=boundary.length,
                subsegment_duration=boundary.end_time - boundary.start_time,
                starts_with_sap=boundary.starts_with_sync,
                sap_type=1 if boundary.starts_with_sync else 0,
                sap_delta=0,
                offset=boundary.offset,
            )
        )
    return tuple(refs)


def compare_references(
    declared: SidxIndex | None, rebuilt: tuple[SidxReference, ...]
) -> tuple[int, int]:
    """Return (checked_refs, mismatches). A missing sidx checks zero refs."""
    if declared is None:
        return 0, 0
    mismatches = 0
    for i, ref in enumerate(declared.references):
        if i >= len(rebuilt):
            mismatches += 1
            continue
        candidate = rebuilt[i]
        if (
            ref.offset != candidate.offset
            or ref.referenced_size != candidate.referenced_size
        ):
            mismatches += 1
    if len(declared.references) != len(rebuilt):
        mismatches += abs(len(declared.references) - len(rebuilt))
    return len(declared.references), mismatches
