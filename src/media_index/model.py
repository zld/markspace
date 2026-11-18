"""Parsed records with source offsets and exact rational time helpers.

Every dataclass stores the absolute byte offset of the structure it came
from, so validation reports can point back at the damaged location. Time
conversions use :class:`Rational` (integer numerator/denominator) and never
floating point.
"""

from __future__ import annotations

from dataclasses import dataclass, field

# Reference type values used inside sidx boxes.
REF_MEDIA = 0
REF_INDEX = 1

# Check status strings are part of the stable JSON output.
STATUS_PASS = "pass"
STATUS_FAIL = "fail"


@dataclass(frozen=True)
class Rational:
    """Exact non-negative rational value used for all time comparisons."""

    numerator: int
    denominator: int

    def __post_init__(self) -> None:
        if self.denominator <= 0:
            raise ValueError("rational denominator must be positive")
        if self.numerator < 0:
            raise ValueError("negative rational time is not representable")

    def as_seconds_tuple(self) -> tuple[int, int]:
        return self.numerator, self.denominator

    def _key(self) -> tuple[int, int]:
        return self.numerator, self.denominator

    def __eq__(self, other: object) -> bool:
        if not isinstance(other, Rational):
            return NotImplemented
        return self.numerator * other.denominator == other.numerator * self.denominator

    def __lt__(self, other: "Rational") -> bool:
        return self.numerator * other.denominator < other.numerator * self.denominator

    def __le__(self, other: "Rational") -> bool:
        return self.numerator * other.denominator <= other.numerator * self.denominator


def ticks_to_rational(ticks: int, timescale: int) -> Rational:
    """Convert ``ticks / timescale`` to an exact :class:`Rational`."""

    if timescale <= 0:
        raise ValueError(f"illegal timescale {timescale}")
    if ticks < 0:
        raise ValueError(f"negative tick count {ticks}")
    return Rational(ticks, timescale)


@dataclass(frozen=True)
class SidxReference:
    """One sidx reference entry; offsets are absolute file positions."""

    order: int  # 1-based position inside the sidx
    reference_type: int  # 0 media, 1 nested index
    referenced_size: int
    subsegment_duration: int  # sidx timescale ticks
    starts_with_sap: bool
    sap_type: int
    sap_delta_time: int
    source_offset: int
    byte_start: int  # resolved anchor + first_offset (absolute)
    byte_end: int  # byte_start + referenced_size


@dataclass(frozen=True)
class SidxBox:
    box_start: int
    source_offset: int  # payload start (after version/flags fields area)
    version: int
    reference_id: int
    timescale: int
    earliest_presentation_time: int
    first_offset: int
    references: tuple[SidxReference, ...]

    @property
    def media_references(self) -> tuple[SidxReference, ...]:
        return tuple(r for r in self.references if r.reference_type == REF_MEDIA)


@dataclass(frozen=True)
class SidxTree:
    """A resolved sidx, including its nested index children (in order)."""

    box: SidxBox
    children: tuple["SidxTree", ...] = ()

    def walk(self) -> "tuple[SidxTree, ...]":
        out: list[SidxTree] = [self]
        for child in self.children:
            out.extend(child.walk())
        return tuple(out)

    def leaf_media_references(self) -> tuple[SidxReference, ...]:
        if not self.children:
            return self.box.media_references
        out: list[SidxReference] = []
        for child in self.children:
            out.extend(child.leaf_media_references())
        return tuple(out)


@dataclass(frozen=True)
class StszBox:
    source_offset: int
    uniform_sample_size: int  # 0 means per-sample sizes follow
    sample_count: int
    entry_sizes: tuple[int, ...]


@dataclass(frozen=True)
class Stz2Box:
    source_offset: int
    field_size: int  # 4, 8 or 16
    sample_count: int
    entry_sizes: tuple[int, ...]


@dataclass(frozen=True)
class SttsEntry:
    sample_count: int
    sample_delta: int
    source_offset: int


@dataclass(frozen=True)
class SttsBox:
    source_offset: int
    entries: tuple[SttsEntry, ...]
    total_samples: int
    total_ticks: int


@dataclass(frozen=True)
class CttsEntry:
    sample_count: int
    sample_offset: int  # signed for version 1
    source_offset: int


@dataclass(frozen=True)
class CttsBox:
    source_offset: int
    version: int
    entries: tuple[CttsEntry, ...]
    total_samples: int


@dataclass(frozen=True)
class StscEntry:
    first_chunk: int  # 1-based per spec
    samples_per_chunk: int
    sample_description_index: int
    source_offset: int


@dataclass(frozen=True)
class StscBox:
    source_offset: int
    entries: tuple[StscEntry, ...]


@dataclass(frozen=True)
class ChunkOffsetBox:
    source_offset: int
    box_type: str  # "stco" or "co64"
    offsets: tuple[int, ...]


@dataclass(frozen=True)
class StssBox:
    source_offset: int
    sync_samples: tuple[int, ...]  # 1-based sample numbers


@dataclass(frozen=True)
class StblTables:
    """All parsed sample-table boxes for one trak, with source positions."""

    stbl_offset: int
    size_table: StszBox | Stz2Box
    stts: SttsBox
    stsc: StscBox
    chunk_offsets: ChunkOffsetBox
    ctts: CttsBox | None = None
    stss: StssBox | None = None

    @property
    def sample_count(self) -> int:
        return self.size_table.sample_count

    @property
    def chunk_count(self) -> int:
        return len(self.chunk_offsets.offsets)


@dataclass(frozen=True)
class CheckResult:
    """One validation outcome; serialized verbatim into JSON output."""

    name: str
    status: str
    source_offset: int
    reason: str = ""
    details: dict = field(default_factory=dict)


@dataclass(frozen=True)
class SegmentBoundary:
    """A rebuilt media segment with its byte range and sample span."""

    order: int
    source_offset: int
    byte_start: int
    byte_end: int
    first_sample: int | None  # 1-based, inclusive; None when empty
    last_sample: int | None
    complete: bool  # every owning chunk sits fully inside the byte range
