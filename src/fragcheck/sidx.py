"""Parsing of 'sidx' segment index boxes (versions 0 and 1).

first_offset is decoded as a *signed* value: a negative offset makes the
first reference point backwards from the end of the sidx box, which is
nonsensical for archival assets and is flagged by the validator rather
than silently wrapped around as an unsigned huge number.
"""
from __future__ import annotations

import struct
from dataclasses import dataclass

from .boxes import StructuralError

MAX_REFERENCES = 1_000_000


class SidxError(StructuralError):
    """Malformed sidx box (CLI exit code 2)."""


@dataclass
class SidxReference:
    reference_type: int  # 0 = direct media reference, 1 = hierarchical (sidx)
    referenced_size: int
    subsegment_duration: int
    starts_with_sap: bool
    sap_type: int
    sap_delta_time: int


@dataclass
class Sidx:
    offset: int
    size: int
    version: int
    reference_id: int
    timescale: int
    earliest_presentation_time: int
    first_offset: int  # signed; relative to end of this sidx box
    references: list

    @property
    def end(self):
        return self.offset + self.size

    def reference_ranges(self):
        """Absolute [start, end) byte ranges covered by each reference."""
        ranges = []
        pos = self.end + self.first_offset
        for ref in self.references:
            ranges.append((pos, pos + ref.referenced_size))
            pos += ref.referenced_size
        return ranges


def parse_sidx(data, box):
    """Parse the sidx box described by a boxes.Box over data."""
    if box.type != "sidx":
        raise SidxError(f"parse_sidx called on {box.type!r} box at offset {box.offset}")
    payload = data[box.data_offset:box.end]
    if len(payload) < 4:
        raise SidxError(f"sidx at offset {box.offset}: truncated full-box header")
    version = payload[0]
    if version not in (0, 1):
        raise SidxError(f"sidx at offset {box.offset}: unsupported version {version}")
    body = payload[4:]
    if version == 0:
        head_fmt, head_size = ">IIiIHH", 20
    else:
        head_fmt, head_size = ">IIQqHH", 28
    if len(body) < head_size:
        raise SidxError(f"sidx at offset {box.offset}: truncated fixed header")
    (reference_id, timescale, ept, first_offset,
     _reserved, ref_count) = struct.unpack_from(head_fmt, body, 0)
    if ref_count > MAX_REFERENCES:
        raise SidxError(
            f"sidx at offset {box.offset}: reference count {ref_count} "
            f"exceeds limit {MAX_REFERENCES}")
    refs_body = body[head_size:]
    if len(refs_body) != ref_count * 12:
        raise SidxError(
            f"sidx at offset {box.offset}: payload holds {len(refs_body)} "
            f"reference bytes, expected {ref_count * 12}")
    references = []
    for i in range(ref_count):
        word, duration, sap = struct.unpack_from(">III", refs_body, i * 12)
        references.append(SidxReference(
            reference_type=word >> 31,
            referenced_size=word & 0x7FFFFFFF,
            subsegment_duration=duration,
            starts_with_sap=bool(sap >> 31),
            sap_type=(sap >> 28) & 0x7,
            sap_delta_time=sap & 0x0FFFFFFF,
        ))
    return Sidx(offset=box.offset, size=box.size, version=version,
                reference_id=reference_id, timescale=timescale,
                earliest_presentation_time=ept, first_offset=first_offset,
                references=references)
