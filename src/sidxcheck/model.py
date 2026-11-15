"""Shared data model: parse results, diagnostics and analysis summaries.

All modules communicate through the dataclasses defined here so that the CLI
layer only has to serialise the final :class:`AnalysisSummary`.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

# ---------------------------------------------------------------------------
# Severity levels
# ---------------------------------------------------------------------------
SEV_FATAL = "fatal"      # the track cannot be parsed safely at all
SEV_ERROR = "error"      # structural/index mismatch; fails strict mode
SEV_WARNING = "warning"  # tolerated anomaly, always reported but never fatal

# ---------------------------------------------------------------------------
# Diagnostic codes.  Keeping them in one place guarantees identical wording
# between the parser, the rebuilder and the test suite.
# ---------------------------------------------------------------------------
BOX_TRUNCATED_HEADER = "box_truncated_header"
BOX_SIZE_BELOW_HEADER = "box_size_below_header"
BOX_BEYOND_FILE = "box_beyond_file"
BOX_SIZE_OVERFLOW = "box_size_overflow"
BOX_PAYLOAD_TRUNCATED = "box_payload_truncated"
BOX_NESTING_TOO_DEEP = "box_nesting_too_deep"
FULLBOX_TRUNCATED = "fullbox_truncated"
ENTRY_COUNT_UNTRUSTED = "entry_count_untrusted"
TABLE_TRUNCATED = "table_truncated"

SIDX_UNKNOWN_VERSION = "sidx_unknown_version"
SIDX_ZERO_TIMESCALE = "sidx_zero_timescale"
SIDX_MISSING = "sidx_missing"
SIDX_DUPLICATE = "sidx_duplicate"

STSZ_MISSING = "stsz_missing"
STSC_MISSING = "stsc_missing"
STCO_MISSING = "stco_missing"
STTS_MISSING = "stts_missing"
STSC_EMPTY = "stsc_empty"
STSC_FIRST_NOT_ONE = "stsc_first_entry_not_one"
STSC_NON_MONOTONIC = "stsc_non_monotonic"
STSC_SAMPLE_DESC_ZERO = "stsc_sample_desc_index_zero"
STCO_DUPLICATE_VARIANT = "stco_duplicate_offset_variant"
SAMPLE_COUNT_INCONSISTENT = "sample_count_inconsistent"
SAMPLE_TABLE_INCOMPLETE = "sample_table_incomplete"

REF_DUPLICATE = "reference_duplicate"
REF_OVERLAP = "reference_overlap"
REF_NON_MONOTONIC = "reference_non_monotonic"
REF_OFFSET_MISMATCH = "reference_offset_mismatch"
REF_OUT_OF_RANGE = "reference_out_of_range"
REF_SIZE_MISMATCH = "reference_size_mismatch"
REF_SAMPLE_COVERAGE_INCOMPLETE = "reference_sample_coverage_incomplete"
CROSS_BOUNDARY_SAMPLE = "cross_boundary_sample"
TRAILING_DATA = "trailing_data"


class CorruptContainerError(Exception):
    """Raised for fatal structural problems when running in strict mode."""


@dataclass(frozen=True)
class Diagnostic:
    """A single, deterministically sortable finding."""

    severity: str
    code: str
    message: str
    box_offset: int | None = None
    box_type: str | None = None
    entry_index: int | None = None

    def sort_key(self) -> tuple[Any, ...]:
        return (
            self.box_offset if self.box_offset is not None else -1,
            self.box_type or "",
            self.entry_index if self.entry_index is not None else -1,
            self.severity,
            self.code,
            self.message,
        )

    def to_dict(self) -> dict[str, Any]:
        out: dict[str, Any] = {
            "severity": self.severity,
            "code": self.code,
            "message": self.message,
        }
        if self.box_offset is not None:
            out["box_offset"] = self.box_offset
        if self.box_type is not None:
            out["box_type"] = self.box_type
        if self.entry_index is not None:
            out["entry_index"] = self.entry_index
        return out


def sort_diagnostics(diags: list[Diagnostic]) -> list[Diagnostic]:
    """Return diagnostics in a stable, input-independent order."""
    return sorted(diags, key=lambda d: d.sort_key())


# ---------------------------------------------------------------------------
# sidx results
# ---------------------------------------------------------------------------
@dataclass(frozen=True)
class SidxReference:
    """One reference entry (reference_type 0 = media fragment)."""

    index: int
    referenced_size: int
    subsegment_duration: int
    starts_with_sap: bool
    sap_type: int
    sap_delta_time: int
    target_offset: int  # resolved absolute file offset of the referenced item

    def to_dict(self) -> dict[str, Any]:
        return {
            "index": self.index,
            "target_offset": self.target_offset,
            "referenced_size": self.referenced_size,
            "subsegment_duration": self.subsegment_duration,
            "starts_with_sap": self.starts_with_sap,
            "sap_type": self.sap_type,
            "sap_delta_time": self.sap_delta_time,
        }


@dataclass
class SidxIndex:
    box_offset: int
    version: int
    reference_id: int
    timescale: int
    earliest_presentation_time: int
    first_offset: int
    references: list[SidxReference] = field(default_factory=list)

    def to_dict(self) -> dict[str, Any]:
        return {
            "box_offset": self.box_offset,
            "version": self.version,
            "reference_id": self.reference_id,
            "timescale": self.timescale,
            "earliest_presentation_time": self.earliest_presentation_time,
            "first_offset": self.first_offset,
            "references": [r.to_dict() for r in self.references],
        }


# ---------------------------------------------------------------------------
# stbl results
# ---------------------------------------------------------------------------
@dataclass
class StscEntry:
    first_chunk: int          # 1-based per ISO BMFF
    samples_per_chunk: int
    sample_description_index: int


@dataclass
class SttsEntry:
    sample_count: int
    sample_delta: int


@dataclass
class SampleTable:
    """Decoded stbl tables plus the per-sample byte layout when complete."""

    sample_sizes: list[int]
    sample_count_field: int          # sample_size field / sum of stsz entries
    uniform_sample_size: int
    stsc_entries: list[StscEntry]
    chunk_offsets: list[int]
    offset_mode: str                 # "stco" | "co64"
    stts_entries: list[SttsEntry]
    total_samples: int
    sample_offsets: list[int] = field(default_factory=list)

    def to_dict(self) -> dict[str, Any]:
        return {
            "sample_count_field": self.sample_count_field,
            "total_samples": self.total_samples,
            "uniform_sample_size": self.uniform_sample_size,
            "chunk_count": len(self.chunk_offsets),
            "stsc_entries": [
                {
                    "first_chunk": e.first_chunk,
                    "samples_per_chunk": e.samples_per_chunk,
                    "sample_description_index": e.sample_description_index,
                }
                for e in self.stsc_entries
            ],
            "stts_entries": [
                {"sample_count": e.sample_count, "sample_delta": e.sample_delta}
                for e in self.stts_entries
            ],
            "offset_mode": self.offset_mode,
        }


# ---------------------------------------------------------------------------
# rebuild / validation results
# ---------------------------------------------------------------------------
@dataclass
class RebuiltSegment:
    """Byte range covered by one sidx reference and the samples inside it."""

    reference_index: int
    start: int
    end: int
    first_sample: int
    last_sample: int

    def to_dict(self) -> dict[str, Any]:
        return {
            "reference_index": self.reference_index,
            "start": self.start,
            "end": self.end,
            "first_sample": self.first_sample,
            "last_sample": self.last_sample,
            "sample_count": max(0, self.last_sample - self.first_sample + 1),
        }


@dataclass
class OffsetCheck:
    reference_index: int
    passed: bool
    expected_offset: int
    actual_offset: int | None = None
    declared_size: int = 0
    end_offset: int = 0

    def to_dict(self) -> dict[str, Any]:
        out = {
            "reference_index": self.reference_index,
            "passed": self.passed,
            "expected_offset": self.expected_offset,
            "declared_size": self.declared_size,
            "end_offset": self.end_offset,
        }
        if self.actual_offset is not None:
            out["actual_offset"] = self.actual_offset
        return out


@dataclass
class AnalysisSummary:
    file_path: str
    file_size: int
    sample_entries: int = 0
    index_refs: int = 0
    offset_check_passed: int = 0
    offset_check_total: int = 0
    rebuilt_segments: list[RebuiltSegment] = field(default_factory=list)
    diagnostics: list[Diagnostic] = field(default_factory=list)
    sidx: SidxIndex | None = None
    sample_table: SampleTable | None = None
    media_end: int = 0
    fatal: bool = False

    @property
    def has_errors(self) -> bool:
        return any(d.severity in (SEV_ERROR, SEV_FATAL) for d in self.diagnostics)

    def to_dict(self) -> dict[str, Any]:
        return {
            "file_path": self.file_path,
            "file_size": self.file_size,
            "sample_entries": self.sample_entries,
            "index_refs": self.index_refs,
            "offset_checks": {
                "passed": self.offset_check_passed,
                "total": self.offset_check_total,
            },
            "rebuilt_segments": [s.to_dict() for s in self.rebuilt_segments],
            "diagnostics": [d.to_dict() for d in self.diagnostics],
            "fatal": self.fatal,
        }
