"""Decode and encode sidx (segment index) boxes.

This is the single index-parsing layer shared by the validator and the
rebuilder, so both sides always agree on the on-disk layout. Only sidx
versions 0 and 1 are decoded; only version 0 is written.
"""
from __future__ import annotations

import struct

from .box import BoxError
from .model import Sidx, SidxReference

_REF_WIDTH = 12


class SidxError(BoxError):
    """Structural error inside a sidx box."""


def decode_sidx(payload: bytes, box_start: int, box_size: int) -> Sidx:
    """Decode a sidx payload; ``box_start``/``box_size`` locate it in the file."""
    if len(payload) < 4:
        raise SidxError("sidx payload too short for version/flags")
    version = payload[0]
    if version == 0:
        fmt = ">IIIIHH"  # ref_id, timescale, ept(32), first_offset(32), reserved, count
    elif version == 1:
        fmt = ">IIIQQHH"  # ref_id, timescale, ept(64), first_offset(64), reserved, count
    else:
        raise SidxError(f"unsupported sidx version {version}")
    fixed = struct.calcsize(fmt)
    if len(payload) < 4 + fixed:
        raise SidxError("sidx payload truncated in fixed fields")
    _ref_id, timescale, ept, first_offset, _reserved, count = struct.unpack_from(
        fmt, payload, 4
    )
    if timescale == 0:
        raise SidxError("sidx timescale must be non-zero")
    available = len(payload) - 4 - fixed
    if available < _REF_WIDTH * count:
        raise SidxError(
            f"sidx declares {count} references but payload holds "
            f"{available // _REF_WIDTH}"
        )
    references = []
    off = 4 + fixed
    for _ in range(count):
        word, duration, sap = struct.unpack_from(">III", payload, off)
        off += _REF_WIDTH
        references.append(
            SidxReference(
                size=word & 0x7FFFFFFF,
                duration=duration,
                starts_with_sap=bool(sap & 0x80000000),
                sap_delta_time=sap & 0x0FFFFFFF,
            )
        )
    return Sidx(
        version=version,
        timescale=timescale,
        earliest_presentation_time=ept,
        first_offset=first_offset,
        references=references,
        box_start=box_start,
        box_size=box_size,
    )


def encode_sidx(
    timescale: int,
    earliest_presentation_time: int,
    first_offset: int,
    references: list[SidxReference],
) -> bytes:
    """Encode a version-0 sidx box (header included) from references."""
    if timescale <= 0:
        raise ValueError("timescale must be positive")
    body = b"".join(
        struct.pack(
            ">III",
            ref.size & 0x7FFFFFFF,
            ref.duration,
            (0x80000000 if ref.starts_with_sap else 0)
            | (ref.sap_delta_time & 0x0FFFFFFF),
        )
        for ref in references
    )
    payload = struct.pack(">I", 0)  # version 0, flags 0
    payload += struct.pack(
        ">IIIIHH",
        1,  # reference_ID
        timescale,
        earliest_presentation_time,
        first_offset,
        0,  # reserved
        len(references),
    )
    payload += body
    return struct.pack(">I4s", 8 + len(payload), b"sidx") + payload
