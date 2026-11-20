"""Minimal, deterministic ISO BMFF box writer used to build test media."""

from __future__ import annotations

import struct
from dataclasses import dataclass


def box(box_type: str, payload: bytes) -> bytes:
    raw_type = box_type.encode("ascii")
    if len(raw_type) != 4:
        raise ValueError(f"box type must be 4 ASCII bytes, got {box_type!r}")
    return struct.pack(">I", 8 + len(payload)) + raw_type + payload


def full_box(box_type: str, version: int, flags: int, payload: bytes) -> bytes:
    if not 0 <= flags <= 0xFFFFFF:
        raise ValueError("flags must fit 24 bits")
    return box(box_type, struct.pack(">B", version) + flags.to_bytes(3, "big") + payload)


def u16(value: int) -> bytes:
    return struct.pack(">H", value)


def u24(value: int) -> bytes:
    return value.to_bytes(3, "big")


def u32(value: int) -> bytes:
    return struct.pack(">I", value)


def s32(value: int) -> bytes:
    return struct.pack(">i", value)


def u64(value: int) -> bytes:
    return struct.pack(">Q", value)


@dataclass(frozen=True)
class SegmentRefSpec:
    size: int
    duration: int
    starts_with_sap: int = 1
    sap_type: int = 1
    sap_delta_time: int = 0
    reference_type: int = 0


def sidx_box(
    *,
    version: int,
    reference_id: int,
    timescale: int,
    earliest_presentation_time: int,
    first_offset: int,
    references: list[SegmentRefSpec],
) -> bytes:
    payload = bytearray()
    payload += struct.pack(">B", version) + b"\x00\x00\x00"
    payload += u32(reference_id) + u32(timescale)
    if version == 0:
        payload += u32(earliest_presentation_time) + u32(first_offset)
    else:
        payload += u64(earliest_presentation_time) + u64(first_offset)
    payload += u16(0) + u16(len(references))
    for ref in references:
        payload += u32((ref.reference_type << 31) | ref.size)
        payload += u32(ref.duration)
        payload += u32(
            (ref.starts_with_sap << 31)
            | ((ref.sap_type & 0x7) << 28)
            | (ref.sap_delta_time & 0x0FFFFFFF)
        )
    return box("sidx", bytes(payload))


def ftpy(handler: str = "iso6") -> bytes:
    return full_box("ftyp", 0, 0, handler.encode("ascii") + u32(0)
                    + b"iso6" + b"iso6" + b"mp41")
