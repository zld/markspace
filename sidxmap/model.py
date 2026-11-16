"""Domain model: exceptions, limits, tracks, samples and index records."""

from __future__ import annotations

from dataclasses import dataclass, field

# Hard safety limits so untrusted files cannot exhaust memory or stack.
DEFAULT_MAX_FILE_BYTES = 256 * 1024 * 1024
DEFAULT_MAX_SAMPLES = 1_000_000
DEFAULT_MAX_NESTING = 16
DEFAULT_MAX_BOXES = 200_000


class SidxmapError(Exception):
    """Base class for every error raised by the package."""

    exit_code = 4


class UsageError(SidxmapError):
    """Bad command line arguments or unreadable/writable files."""

    exit_code = 4


class StructureError(SidxmapError):
    """The ISO BMFF container itself is malformed or truncated."""

    exit_code = 2


class MappingError(SidxmapError):
    """Sample tables or declared sidx references are inconsistent."""

    exit_code = 3


@dataclass(frozen=True)
class Sample:
    """One media sample located by absolute file byte range."""

    index: int
    offset: int
    size: int
    duration: int
    dts: int
    cts_delta: int = 0
    is_sync: bool = True

    @property
    def end(self) -> int:
        return self.offset + self.size

    @property
    def pts(self) -> int:
        return self.dts + self.cts_delta


@dataclass
class Track:
    track_id: int
    handler: str
    timescale: int
    samples: list[Sample] = field(default_factory=list)
    has_stss: bool = False

    @property
    def sample_count(self) -> int:
        return len(self.samples)

    def duration(self) -> int:
        return sum(s.duration for s in self.samples)


@dataclass(frozen=True)
class MdatExtent:
    """Absolute byte range occupied by one mdat payload."""

    offset: int
    size: int

    @property
    def end(self) -> int:
        return self.offset + self.size


@dataclass(frozen=True)
class SidxReference:
    """One reference entry as declared by (or rebuilt for) a sidx box."""

    referenced_type: int
    referenced_size: int
    subsegment_duration: int
    starts_with_sap: bool
    sap_type: int
    sap_delta: int
    offset: int = 0

    @property
    def end(self) -> int:
        return self.offset + self.referenced_size


@dataclass(frozen=True)
class SidxIndex:
    timescale: int
    earliest_presentation_time: int
    first_offset: int
    reference_id: int
    references: tuple[SidxReference, ...]
    anchor_offset: int


@dataclass(frozen=True)
class Boundary:
    """One reconstructed byte range for a single track."""

    track_id: int
    index: int
    offset: int
    length: int
    start_time: int
    end_time: int
    sample_first: int
    sample_last: int
    starts_with_sync: bool

    def as_dict(self) -> dict:
        return {
            "track_id": self.track_id,
            "boundary_index": self.index,
            "offset": self.offset,
            "length": self.length,
            "start_time": self.start_time,
            "end_time": self.end_time,
            "sample_first": self.sample_first,
            "sample_last": self.sample_last,
            "starts_with_sync": self.starts_with_sync,
        }


@dataclass
class MediaInfo:
    tracks: list[Track]
    mdats: list[MdatExtent]
    sidx: SidxIndex | None
    file_size: int
