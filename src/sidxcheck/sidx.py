"""Parsing of ``sidx`` (segment index) boxes, version 0 and version 1.

The parser resolves every reference to an absolute file offset while it walks
the entries, so the validator can consume the same results without repeating
any offset arithmetic.
"""

from __future__ import annotations

import struct

from . import model
from .boxes import LocatedBox, read_fullbox_prefix

# Fixed fields after the version/flags prefix, excluding reference entries.
_V0_FIXED = 4 + 4 + 4 + 4 + 4      # ref_id, timescale, pts, first_offset, reserved
_V1_FIXED = 4 + 4 + 8 + 8 + 4      # 64-bit presentation time / first offset
# One reference entry: reference_size, subsegment_duration, SAP info.
_ENTRY_SIZE = 4 + 4 + 4

# An entry count claiming more entries than the payload can hold is treated as
# untrusted rather than used to allocate memory.
MAX_SIDX_ENTRIES = 1 << 28


def parse_sidx(located: LocatedBox, scan) -> model.SidxIndex | None:
    """Parse one buffered sidx box.

    ``scan`` is a :class:`~sidxcheck.boxes._BoxScan`-like object carrying the
    diagnostic list and strict-mode behaviour; the validator thus reuses the
    exact same error classification as the parser.
    """
    header, payload = located.header, located.payload
    prefix = read_fullbox_prefix(payload, header, scan)
    if prefix is None:
        return None
    version, _flags, pos = prefix

    if version not in (0, 1):
        scan.fail(
            model.SIDX_UNKNOWN_VERSION,
            f"sidx at offset {header.box_offset} uses unsupported version "
            f"{version}; only 0 and 1 are understood",
            box_offset=header.box_offset,
            box_type="sidx",
        )
        return None

    fixed_size = _V0_FIXED if version == 0 else _V1_FIXED
    if len(payload) - pos < fixed_size:
        scan.fail(
            model.TABLE_TRUNCATED,
            f"sidx at offset {header.box_offset} is truncated inside its "
            "fixed fields",
            box_offset=header.box_offset,
            box_type="sidx",
        )
        return None

    if version == 0:
        reference_id, timescale, pts, first_offset, reserved = struct.unpack_from(
            ">IIIII", payload, pos
        )
    else:
        reference_id, timescale = struct.unpack_from(">II", payload, pos)
        pts, first_offset, reserved = struct.unpack_from(
            ">QQI", payload, pos + 8
        )

    pos += fixed_size
    if reserved != 0:
        scan.fail(
            model.SIDX_UNKNOWN_VERSION,
            f"sidx at offset {header.box_offset} has non-zero reserved field "
            f"{reserved}",
            box_offset=header.box_offset,
            box_type="sidx",
            severity=model.SEV_WARNING,
        )

    index = model.SidxIndex(
        box_offset=header.box_offset,
        version=version,
        reference_id=reference_id,
        timescale=timescale,
        earliest_presentation_time=pts,
        first_offset=first_offset,
    )

    if timescale == 0:
        scan.fail(
            model.SIDX_ZERO_TIMESCALE,
            f"sidx at offset {header.box_offset} declares timescale 0",
            box_offset=header.box_offset,
            box_type="sidx",
        )
        # Offsets are still resolvable, so parsing continues in lenient mode.

    if len(payload) - pos < 4:
        scan.fail(
            model.TABLE_TRUNCATED,
            f"sidx at offset {header.box_offset} has no reference_count field",
            box_offset=header.box_offset,
            box_type="sidx",
        )
        return None
    (entry_count,) = struct.unpack_from(">I", payload, pos)
    pos += 4

    available = len(payload) - pos
    needed = entry_count * _ENTRY_SIZE
    if entry_count > MAX_SIDX_ENTRIES or needed > available:
        scan.fail(
            model.ENTRY_COUNT_UNTRUSTED,
            f"sidx at offset {header.box_offset} reference_count "
            f"{entry_count} cannot fit in {available} remaining payload bytes",
            box_offset=header.box_offset,
            box_type="sidx",
        )
        return None

    # Offset resolution: first reference is anchored at the sidx box itself
    # plus first_offset; later references follow the previous referenced item.
    anchor = header.box_offset + first_offset
    if anchor < 0 or anchor > 1 << 62:
        scan.fail(
            model.REF_OUT_OF_RANGE,
            f"sidx at offset {header.box_offset} resolves its first reference "
            f"to untrusted offset {anchor}",
            box_offset=header.box_offset,
            box_type="sidx",
        )
        return None
    for i in range(entry_count):
        size_word, duration, sap_word = struct.unpack_from(
            ">III", payload, pos
        )
        pos += _ENTRY_SIZE
        reference_type = (size_word >> 31) & 1
        referenced_size = size_word & 0x7FFFFFFF
        subsegment_duration = duration
        sap_flag = bool((sap_word >> 31) & 1)
        sap_type = (sap_word >> 28) & 0x7
        sap_delta = sap_word & 0x0FFFFFFF

        target = anchor
        if referenced_size > (1 << 62) - anchor:
            scan.fail(
                model.BOX_SIZE_OVERFLOW,
                f"sidx reference {i} at offset {header.box_offset} overflows "
                "the 64-bit offset space",
                box_offset=header.box_offset,
                box_type="sidx",
                entry_index=i,
            )
            return None
        index.references.append(
            model.SidxReference(
                index=i,
                referenced_size=referenced_size,
                subsegment_duration=subsegment_duration,
                starts_with_sap=sap_flag,
                sap_type=sap_type,
                sap_delta_time=sap_delta,
                target_offset=target,
            )
        )
        if reference_type == 1:
            scan.fail(
                model.SIDX_UNKNOWN_VERSION,
                f"sidx reference {i} at offset {header.box_offset} is an index "
                "reference (reference_type=1); only media references are "
                "supported",
                box_offset=header.box_offset,
                box_type="sidx",
                entry_index=i,
                severity=model.SEV_WARNING,
            )
        anchor += referenced_size

    return index


def parse_all(boxes, scan) -> model.SidxIndex | None:
    """Parse every buffered sidx box, flagging duplicates deterministically."""
    indexes: list[model.SidxIndex] = []
    for located in boxes:
        parsed = parse_sidx(located, scan)
        if parsed is not None:
            indexes.append(parsed)
    if not indexes:
        return None
    chosen = min(indexes, key=lambda idx: idx.box_offset)
    for extra in indexes:
        if extra is not chosen:
            scan.fail(
                model.SIDX_DUPLICATE,
                f"duplicate sidx at offset {extra.box_offset}; using the one "
                f"at {chosen.box_offset}",
                box_offset=extra.box_offset,
                box_type="sidx",
                severity=model.SEV_WARNING,
            )
    return chosen
