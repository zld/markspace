"""Comparison result records shared by the comparer, CLI and store."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

from .model import SniffResult

_FLOAT_EPSILON = 1e-9


@dataclass(slots=True)
class Mismatch:
    code: str
    path: str
    detail: str
    expected: Any = None
    actual: Any = None
    tracks: list[int] = field(default_factory=list)

    def to_dict(self) -> dict[str, Any]:
        return {
            "code": self.code,
            "path": self.path,
            "detail": self.detail,
            "expected": self.expected,
            "actual": self.actual,
            "tracks": list(self.tracks),
        }


@dataclass(slots=True)
class FileReport:
    path: str
    expected: SniffResult | None
    actual: SniffResult
    mismatches: list[Mismatch] = field(default_factory=list)
    expected_scan_id: int | None = None
    actual_scan_id: int | None = None
    # Signed deviations, tracked even when inside the tolerance so the
    # summary can report the observed maximum.
    duration_deviations_ms: list[float] = field(default_factory=list)

    @property
    def has_error(self) -> bool:
        return self.actual.error is not None

    def to_dict(self) -> dict[str, Any]:
        return {
            "path": self.path,
            "container": self.actual.container,
            "expected": self.expected.to_dict() if self.expected else None,
            "actual": self.actual.to_dict(),
            "mismatches": [m.to_dict() for m in self.mismatches],
        }


@dataclass(slots=True)
class CompareReport:
    files: list[FileReport] = field(default_factory=list)
    errors: list[dict[str, Any]] = field(default_factory=list)
    duration_tolerance_ms: float = 0.0

    @property
    def files_scanned(self) -> int:
        return len(self.files)

    @property
    def tracks_expected(self) -> int:
        return sum(
            len(f.expected.tracks) for f in self.files if f.expected is not None
        )

    @property
    def tracks_actual(self) -> int:
        return sum(len(f.actual.tracks) for f in self.files)

    @property
    def duration_deviation_max_ms(self) -> float | None:
        values = [
            abs(value)
            for f in self.files
            for value in f.duration_deviations_ms
        ]
        return round(max(values), 3) if values else 0.0

    @property
    def mismatches(self) -> list[Mismatch]:
        return [m for f in self.files for m in f.mismatches]

    @property
    def ok(self) -> bool:
        return not self.errors and not self.mismatches
