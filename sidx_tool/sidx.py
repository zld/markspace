"""Segment Index Box (SIDX), ISO/IEC 14496-12 section 8.16.3.

Supports version 0 (32-bit offsets/times) and version 1 (64-bit).  A stale or
damaged SIDX never blocks a rebuild: callers catch :class:`SidxParseError` and
fall back to regenerating the index from sample tables.
"""

from __future__ import annotations

import struct
from dataclasses import dataclass

from .boxes import Box, read_fullbox_payload
from .errors import FormatError
from .sample_tables import SampleTable
from typing import BinaryIO


class SidxParseError(FormatError):
    """Existing SIDX is missing or its fields are damaged."""


@dataclass
class SidxReference:
    reference_type: int  # 0 = media, 1 = sidx (chaining, unsupported on write)
    referenced_size: int
    subsegment_duration: int
    starts_with_sap: bool
    sap_type: int
    sap_delta_time: int
    # Decoded convenience fields (not part of the wire format).
    chunk_offset: int = 0  # reference_type == 0 only
    first_bit: int = 0  # reference_type == 1 only

    def sap_flags(self) -> tuple[int, int, int]:
        return (1 if self.starts_with_sap else 0), self.sap_type, self.sap_delta_time


@dataclass
class Sidx:
    version: int
    flags: int
    reference_id: int
    timescale: int
    earliest_presentation_time: int
    first_offset: int
    references: list[SidxReference]

    def serialize(self, anchor_offset: int) -> bytes:
        """Serialize as version 0/1 depending on actual field magnitudes."""
        use_64 = (
            self.earliest_presentation_time > 0xFFFFFFFF
            or self.first_offset > 0xFFFFFFFF
            or any(r.subsegment_duration > 0xFFFFFFFF or r.chunk_offset > 0xFFFFFFFF for r in self.references)
        )
        version = 1 if use_64 else 0
        time_width = 8 if version == 1 else 4
        body = struct.pack(">II", self.reference_id, self.timescale)
        if version == 1:
            body += struct.pack(">QQ", self.earliest_presentation_time, self.first_offset)
        else:
            body += struct.pack(">II", self.earliest_presentation_time, self.first_offset)
        body += struct.pack(">HH", 0, len(self.references))  # reserved + reference_count
        for ref in self.references:
            if ref.reference_type != 0:
                raise FormatError("serializing chained SIDX references is not supported")
            first_word = (ref.reference_type << 31) | (ref.referenced_size & 0x7FFFFFFF)
            sap_bit, sap_type, sap_delta = ref.sap_flags()
            sap_word = (sap_bit << 31) | ((sap_type & 0x7F) << 24) | (sap_delta & 0x00FFFFFF)
            body += struct.pack(">I", first_word)
            body += struct.pack(">Q" if time_width == 8 else ">I", ref.subsegment_duration)
            body += struct.pack(">I", sap_word)
        header = struct.pack(">I4s", 8 + 4 + len(body), b"sidx")
        header += bytes([version]) + self.flags.to_bytes(3, "big")
        return header + body

    def byte_ranges(self, sidx_box_start: int) -> list[tuple[int, int]]:
        """Absolute [start, end) byte ranges covered by media references.

        The first reference starts at ``sidx_box_start + first_offset`` and
        every later reference starts exactly where the previous one ended.
        """
        cursor = sidx_box_start + self.first_offset
        ranges: list[tuple[int, int]] = []
        for ref in self.references:
            if ref.reference_type != 0:
                raise SidxParseError("chained SIDX references are not supported")
            start = cursor + ref.chunk_offset
            end = start + ref.referenced_size
            ranges.append((start, end))
            cursor = end
        return ranges


def parse_sidx(stream: BinaryIO, box: Box) -> Sidx:
    version, flags, data = read_fullbox_payload(stream, box)
    if version not in (0, 1):
        raise SidxParseError(f"unsupported SIDX version {version} at {box.path}")
    width = 8 if version == 1 else 4
    min_head = 8 + 2 * width + 4
    if len(data) < min_head:
        raise SidxParseError(f"SIDX payload truncated at {box.path}")
    reference_id, timescale = struct.unpack(">II", data[:8])
    if timescale <= 0:
        raise SidxParseError(f"SIDX non-positive timescale at {box.path}")
    if version == 1:
        ept, first_offset = struct.unpack(">QQ", data[8:24])
        pos = 24
    else:
        ept, first_offset = struct.unpack(">II", data[8:16])
        pos = 16
    _reserved, ref_count = struct.unpack(">HH", data[pos:pos + 4])
    pos += 4
    if ref_count == 0:
        raise SidxParseError(f"SIDX with zero references at {box.path}")
    record = 8 + width
    if len(data) - pos != ref_count * record:
        raise SidxParseError(f"SIDX reference payload size mismatch at {box.path}")
    references: list[SidxReference] = []
    for _ in range(ref_count):
        word1 = struct.unpack(">I", data[pos:pos + 4])[0]
        ref_type = word1 >> 31
        ref_size = word1 & 0x7FFFFFFF
        if width == 8:
            duration = struct.unpack(">Q", data[pos + 4:pos + 12])[0]
            sap_word = struct.unpack(">I", data[pos + 12:pos + 16])[0]
        else:
            duration = struct.unpack(">I", data[pos + 4:pos + 8])[0]
            sap_word = struct.unpack(">I", data[pos + 8:pos + 12])[0]
        if ref_type == 0 and ref_size <= 0:
            raise SidxParseError("SIDX media reference with non-positive size")
        starts_sap = bool(sap_word >> 31)
        sap_type = (sap_word >> 24) & 0x7F
        sap_delta = sap_word & 0x00FFFFFF
        chunk_offset, first_bit = 0, 0
        if ref_type == 1:
            first_bit = ref_size
        references.append(
            SidxReference(ref_type, ref_size, duration, starts_sap, sap_type, sap_delta, chunk_offset, first_bit)
        )
        pos += record
    return Sidx(version, flags, reference_id, timescale, ept, first_offset, references)


def build_sidx(
    tables: list[SampleTable],
    timescale: int,
    segment_ranges: list[tuple[int, int]],
    segment_times: list[tuple[int, int]],
    sidx_box_start: int,
) -> Sidx:
    """Construct a fresh SIDX whose each reference spans one segment's bytes.

    ``segment_ranges`` are absolute file offsets planned for the output.
    Times in ``segment_times`` are already expressed in *timescale* units.
    """
    video = next((t for t in tables if t.handler == "vide"), tables[0])
    first_abs_start = segment_ranges[0][0]
    references: list[SidxReference] = []
    cursor = first_abs_start
    for (byte_start, byte_end), (t_start, t_end) in zip(segment_ranges, segment_times):
        references.append(
            SidxReference(
                reference_type=0,
                referenced_size=byte_end - byte_start,
                subsegment_duration=t_end - t_start,
                starts_with_sap=True,
                sap_type=1,
                sap_delta_time=0,
                chunk_offset=byte_start - cursor,
            )
        )
        cursor = byte_end
    needs_64 = first_abs_start - sidx_box_start > 0xFFFFFFFF or any(
        r.subsegment_duration > 0xFFFFFFFF for r in references
    )
    return Sidx(
        version=1 if needs_64 else 0,
        flags=0,
        reference_id=video.track_id,
        timescale=timescale,
        earliest_presentation_time=segment_times[0][0],
        first_offset=first_abs_start - sidx_box_start,
        references=references,
    )
