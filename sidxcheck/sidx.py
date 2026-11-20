"""SIDX (Segment Index) parse / serialize helpers shared by checker and rewriter."""

from __future__ import annotations

import struct
from dataclasses import dataclass, field
from typing import BinaryIO

from .boxes import Box, read_payload
from .errors import IndexValidationError, MediaStructureError

SIDX_REFERENCE_SIZE_LIMIT = (1 << 31) - 1
SIDX_ENTRY_SIZE = 12
SIDX_FIXED_PAYLOAD_V0 = 4 + 4 + 4 + 4 + 4 + 2 + 2
SIDX_FIXED_PAYLOAD_V1 = 4 + 4 + 4 + 8 + 8 + 2 + 2


@dataclass(frozen=True)
class SidxReference:
    reference_type: int
    referenced_size: int
    subsegment_duration: int
    starts_with_sap: int
    sap_type: int
    sap_delta_time: int


@dataclass
class Sidx:
    version: int
    reference_id: int
    timescale: int
    earliest_presentation_time: int
    first_offset: int
    references: list[SidxReference] = field(default_factory=list)
    box_start: int = 0
    box_end: int = 0

    @property
    def box_size(self) -> int:
        return self.box_end - self.box_start

    @property
    def payload_size(self) -> int:
        return self.serialized_payload_size(self.version, len(self.references))

    @property
    def total_duration(self) -> int:
        return sum(ref.subsegment_duration for ref in self.references)

    @property
    def anchor_offset(self) -> int:
        """Absolute file offset of the first referenced byte."""
        return self.box_end + self.first_offset

    def ranges(self) -> list[tuple[int, int]]:
        out: list[tuple[int, int]] = []
        position = self.anchor_offset
        for ref in self.references:
            out.append((position, position + ref.referenced_size))
            position += ref.referenced_size
        return out

    @staticmethod
    def serialized_payload_size(version: int, reference_count: int) -> int:
        fixed = SIDX_FIXED_PAYLOAD_V0 if version == 0 else SIDX_FIXED_PAYLOAD_V1
        return fixed + SIDX_ENTRY_SIZE * reference_count

    @staticmethod
    def serialized_box_size(version: int, reference_count: int) -> int:
        return 8 + Sidx.serialized_payload_size(version, reference_count)


def _require(payload_size: int, needed: int, what: str) -> None:
    if payload_size < needed:
        raise MediaStructureError(
            f"SIDX payload is {payload_size} bytes but needs at least {needed} for {what}",
            location="sidx",
        )


def parse_sidx_payload(payload: bytes, *, box_start: int, box_end: int) -> Sidx:
    size = len(payload)
    _require(size, 4, "version/flags")
    version, flags = struct.unpack(">B3s", payload[:4])
    if version not in (0, 1):
        raise MediaStructureError(f"unsupported SIDX version {version}", location="sidx")
    if flags != b"\x00\x00\x00":
        raise MediaStructureError(
            f"SIDX has non-zero flags {flags.hex()}", location="sidx"
        )
    fixed = SIDX_FIXED_PAYLOAD_V0 if version == 0 else SIDX_FIXED_PAYLOAD_V1
    _require(size, fixed, "fixed fields")
    if version == 0:
        (
            reference_id,
            timescale,
            earliest_presentation_time,
            first_offset,
        ) = struct.unpack(">IIII", payload[4:20])
        cursor = 20
    else:
        (
            reference_id,
            timescale,
            earliest_presentation_time,
            first_offset,
        ) = struct.unpack(">IIQQ", payload[4:28])
        cursor = 28
    (reserved, reference_count) = struct.unpack(">HH", payload[cursor : cursor + 4])
    if reserved != 0:
        raise MediaStructureError(
            f"SIDX reserved field is {reserved}, expected 0", location="sidx"
        )
    if timescale == 0:
        raise MediaStructureError("SIDX timescale is 0", location="sidx")
    needed = fixed + SIDX_ENTRY_SIZE * reference_count
    if size < needed:
        raise MediaStructureError(
            f"SIDX declares {reference_count} references needing {needed} bytes, "
            f"only {size} available",
            location="sidx",
        )
    if size > needed:
        raise MediaStructureError(
            f"SIDX has {size - needed} trailing byte(s) after {reference_count} references",
            location="sidx",
        )
    references: list[SidxReference] = []
    for index in range(reference_count):
        base = cursor + 4 + index * SIDX_ENTRY_SIZE
        raw = struct.unpack(">III", payload[base : base + SIDX_ENTRY_SIZE])
        word = raw[0]
        references.append(
            SidxReference(
                reference_type=(word >> 31) & 0x1,
                referenced_size=word & 0x7FFFFFFF,
                subsegment_duration=raw[1],
                starts_with_sap=(raw[2] >> 31) & 0x1,
                sap_type=(raw[2] >> 28) & 0x7,
                sap_delta_time=raw[2] & 0x0FFFFFFF,
            )
        )
    return Sidx(
        version=version,
        reference_id=reference_id,
        timescale=timescale,
        earliest_presentation_time=earliest_presentation_time,
        first_offset=first_offset,
        references=references,
        box_start=box_start,
        box_end=box_end,
    )


def parse_sidx_box(fp: BinaryIO, box: Box) -> Sidx:
    if box.type != "sidx":
        raise ValueError("parse_sidx_box requires a sidx box")
    payload = read_payload(fp, box)
    return parse_sidx_payload(payload, box_start=box.start, box_end=box.end)


def choose_version(
    *, reference_id: int, timescale: int, earliest_presentation_time: int,
    first_offset: int, references: list[SidxReference],
) -> int:
    needs_v1 = (
        earliest_presentation_time > 0xFFFFFFFF
        or first_offset > 0xFFFFFFFF
        or len(references) > 0xFFFF
    )
    return 1 if needs_v1 else 0


def serialize_sidx(sidx: Sidx) -> bytes:
    version = sidx.version
    if version not in (0, 1):
        raise IndexValidationError(f"cannot serialize SIDX version {version}", location="sidx")
    if not 0 <= sidx.reference_id <= 0xFFFFFFFF:
        raise IndexValidationError(
            f"reference_ID {sidx.reference_id} does not fit 32 bits", location="sidx"
        )
    if not 0 < sidx.timescale <= 0xFFFFFFFF:
        raise IndexValidationError(
            f"timescale {sidx.timescale} must fit 1..2^32-1", location="sidx"
        )
    if len(sidx.references) > 0xFFFF:
        raise IndexValidationError(
            f"{len(sidx.references)} references exceed the 65535 SIDX limit", location="sidx"
        )
    max_field = 0xFFFFFFFF if version == 0 else 0xFFFFFFFFFFFFFFFF
    for name, value in (
        ("earliest_presentation_time", sidx.earliest_presentation_time),
        ("first_offset", sidx.first_offset),
    ):
        if not 0 <= value <= max_field:
            raise IndexValidationError(
                f"{name}={value} does not fit SIDX version {version}", location="sidx"
            )
    body = bytearray()
    body += struct.pack(">B3s", version, b"\x00\x00\x00")
    body += struct.pack(">II", sidx.reference_id, sidx.timescale)
    if version == 0:
        body += struct.pack(">II", sidx.earliest_presentation_time, sidx.first_offset)
    else:
        body += struct.pack(">QQ", sidx.earliest_presentation_time, sidx.first_offset)
    body += struct.pack(">HH", 0, len(sidx.references))
    for index, ref in enumerate(sidx.references):
        if ref.reference_type not in (0, 1):
            raise IndexValidationError(
                f"reference {index} has invalid type {ref.reference_type}", location="sidx"
            )
        if not 0 <= ref.referenced_size <= SIDX_REFERENCE_SIZE_LIMIT:
            raise IndexValidationError(
                f"reference {index} size {ref.referenced_size} exceeds the 31-bit SIDX limit",
                location="sidx",
            )
        if not 0 <= ref.subsegment_duration <= 0xFFFFFFFF:
            raise IndexValidationError(
                f"reference {index} duration {ref.subsegment_duration} does not fit 32 bits",
                location="sidx",
            )
        if not 0 < ref.sap_type < 8:
            raise IndexValidationError(
                f"reference {index} has invalid SAP type {ref.sap_type}", location="sidx"
            )
        if not 0 <= ref.sap_delta_time <= 0x0FFFFFFF:
            raise IndexValidationError(
                f"reference {index} SAP delta {ref.sap_delta_time} does not fit 28 bits",
                location="sidx",
            )
        word = ((ref.reference_type & 0x1) << 31) | ref.referenced_size
        sap_word = (
            ((ref.starts_with_sap & 0x1) << 31)
            | ((ref.sap_type & 0x7) << 28)
            | (ref.sap_delta_time & 0x0FFFFFFF)
        )
        body += struct.pack(">III", word, ref.subsegment_duration, sap_word)
    total = 8 + len(body)
    return struct.pack(">I4s", total, b"sidx") + bytes(body)


def validate_sidx_structure(sidx: Sidx, *, max_references: int = 100_000) -> list[str]:
    """Return structural problems that do not require sample-table context."""
    problems: list[str] = []
    if not sidx.references:
        problems.append("SIDX has no references")
    if len(sidx.references) > max_references:
        problems.append(
            f"SIDX has {len(sidx.references)} references, limit is {max_references}"
        )
    for index, ref in enumerate(sidx.references):
        where = f"reference[{index}]"
        if ref.reference_type == 1:
            problems.append(
                f"{where} is hierarchical (nested SIDX), which cannot be expanded here"
            )
        if ref.referenced_size == 0:
            problems.append(f"{where} has zero referenced_size")
        if ref.referenced_size > SIDX_REFERENCE_SIZE_LIMIT:
            problems.append(
                f"{where} referenced_size {ref.referenced_size} exceeds 31 bits"
            )
        if ref.subsegment_duration == 0:
            problems.append(f"{where} has zero subsegment_duration")
        if ref.starts_with_sap == 1 and ref.sap_type == 0:
            problems.append(f"{where} starts_with_SAP=1 but SAP_type=0")
        if ref.starts_with_sap == 0 and ref.sap_type != 0:
            problems.append(f"{where} starts_with_SAP=0 but SAP_type={ref.sap_type}")
        if ref.sap_type > 6:
            problems.append(f"{where} has reserved SAP_type {ref.sap_type}")
    if sidx.anchor_offset < 0:
        problems.append(f"SIDX first reference offset {sidx.anchor_offset} is negative")
    return problems
