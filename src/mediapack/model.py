"""Shared data model: samples, fragment index entries, diagnostics.

Both parsers (sidx, stbl) produce these types; the validator and the
rebuilder consume them so they always agree on the same structures.
"""
from __future__ import annotations

from dataclasses import dataclass, field


@dataclass(frozen=True)
class Sample:
    index: int  # 1-based sample number
    offset: int  # absolute file offset of the sample
    size: int
    dts: int  # decode time in timescale units
    duration: int  # duration in timescale units
    is_sync: bool


@dataclass
class SampleTable:
    timescale: int
    samples: list[Sample]
    chunk_offsets: list[int]
    offset_width: int  # 4 for stco, 8 for co64
    stts_entries: list[tuple[int, int]]
    stsc_entries: list[tuple[int, int, int]]
    sync_indices: list[int]

    @property
    def sample_count(self) -> int:
        return len(self.samples)

    @property
    def total_duration(self) -> int:
        return sum(s.duration for s in self.samples)


@dataclass(frozen=True)
class SidxReference:
    size: int
    duration: int
    starts_with_sap: bool
    sap_delta_time: int


@dataclass
class Sidx:
    version: int
    timescale: int
    earliest_presentation_time: int
    first_offset: int
    references: list[SidxReference]
    box_start: int
    box_size: int

    @property
    def box_end(self) -> int:
        return self.box_start + self.box_size


@dataclass(frozen=True)
class Fragment:
    start_sample: int  # 1-based, inclusive
    end_sample: int  # 1-based, inclusive
    duration: int  # actual duration in timescale units
    delta: int  # actual duration minus target, in timescale units


@dataclass(frozen=True)
class Diagnostic:
    severity: str  # "error" | "warning"
    code: str
    message: str


@dataclass
class ValidationReport:
    path: str
    diagnostics: list[Diagnostic] = field(default_factory=list)
    sample_count: int = 0
    timescale: int = 0
    index_offset_check: str = "ABSENT"  # PASS | STALE | ABSENT
    table: SampleTable | None = None
    sidx: Sidx | None = None

    def error(self, code: str, message: str) -> None:
        self.diagnostics.append(Diagnostic("error", code, message))

    def warning(self, code: str, message: str) -> None:
        self.diagnostics.append(Diagnostic("warning", code, message))

    @property
    def ok(self) -> bool:
        """True when no error-severity diagnostics were recorded.

        A stale, truncated or missing sidx is a recoverable warning; sample
        table / media mismatches are errors.
        """
        return all(d.severity != "error" for d in self.diagnostics)
