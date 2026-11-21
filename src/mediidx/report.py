"""Validation report model produced by the independent cross-check."""

from __future__ import annotations

from dataclasses import dataclass, field


@dataclass
class ValidationReport:
    source: str
    sample_entries: int = 0
    reference_count: int = 0
    boundaries: list[list[int]] = field(default_factory=list)
    offset_checks: list[dict] = field(default_factory=list)
    duration_check: dict = field(default_factory=dict)
    coverage_check: dict = field(default_factory=dict)
    errors: list[str] = field(default_factory=list)
    warnings: list[str] = field(default_factory=list)
    timescale: int = 0
    earliest_presentation_time: int = 0
    index_version: int = 0

    @property
    def ok(self) -> bool:
        return not self.errors and all(
            check["status"] == "ok" for check in self.offset_checks
        )

    @property
    def index_offset_check(self) -> str:
        return "ok" if self.ok else "mismatch"

    def to_dict(self) -> dict:
        return {
            "command": "validate",
            "source": self.source,
            "sample_entries": self.sample_entries,
            "reference_count": self.reference_count,
            "boundaries": self.boundaries,
            "index_offset_check": self.index_offset_check,
            "offset_checks": self.offset_checks,
            "duration_check": self.duration_check,
            "coverage_check": self.coverage_check,
            "timescale": self.timescale,
            "earliest_presentation_time": self.earliest_presentation_time,
            "index_version": self.index_version,
            "errors": self.errors,
            "warnings": self.warnings,
        }
