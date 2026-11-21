"""Shared data model for parsed sample tables, sidx boxes and segment plans."""

from __future__ import annotations

from dataclasses import dataclass, field


@dataclass(frozen=True)
class Sample:
    index: int
    offset: int
    size: int
    duration: int
    dts: int
    chunk_index: int

    @property
    def end(self) -> int:
        return self.offset + self.size


@dataclass(frozen=True)
class Chunk:
    index: int
    offset: int
    size: int
    first_sample: int
    sample_count: int

    @property
    def end(self) -> int:
        return self.offset + self.size

    @property
    def sample_end(self) -> int:
        return self.first_sample + self.sample_count


@dataclass
class Track:
    track_id: int
    timescale: int
    handler: bytes
    samples: list[Sample]
    chunks: list[Chunk]
    mdat_start: int
    mdat_end: int
    offsets_box: bytes  # b"stco" or b"co64"

    @property
    def sample_count(self) -> int:
        return len(self.samples)

    @property
    def duration(self) -> int:
        return sum(sample.duration for sample in self.samples)

    @property
    def media_start(self) -> int:
        return self.chunks[0].offset if self.chunks else 0

    @property
    def media_end(self) -> int:
        return self.chunks[-1].end if self.chunks else 0


@dataclass(frozen=True)
class SidxReference:
    index: int
    reference_type: int
    referenced_size: int
    subsegment_duration: int
    starts_with_sap: int
    sap_type: int
    sap_delta_time: int

    @property
    def is_media(self) -> bool:
        return self.reference_type == 0


@dataclass
class Sidx:
    version: int
    reference_id: int
    timescale: int
    earliest_presentation_time: int
    first_offset: int
    references: list[SidxReference]
    box_start: int
    box_end: int

    @property
    def reference_count(self) -> int:
        return len(self.references)

    @property
    def explicit_earliest(self) -> bool:
        # Version 0/1 both carry a field, but a zero value is the common
        # "not recorded" state; rebuilds preserve the field verbatim anyway.
        return True


@dataclass(frozen=True)
class Segment:
    index: int
    chunk_first: int
    chunk_last: int
    sample_first: int
    sample_last: int
    byte_start: int
    byte_end: int
    start_time: int
    duration: int
    references: int
    isolated_reason: str | None = None

    @property
    def sample_range(self) -> tuple[int, int]:
        return (self.sample_first, self.sample_last)

    @property
    def byte_range(self) -> tuple[int, int]:
        return (self.byte_start, self.byte_end)


@dataclass
class Layout:
    track: Track
    segments: list[Segment]
    target_duration: int
    notes: list[str] = field(default_factory=list)
