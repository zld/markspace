"""Shared records passed between decoding, timeline repair and reporting.

Everything downstream of the decoder works on these immutable records, which is
what lets the terminal and JSON sinks render the exact same numbers instead of
each recomputing timestamps.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum
from fractions import Fraction
from typing import Sequence


class RepairKind(str, Enum):
    """Why a frame's presentation timestamp had to be rewritten."""

    NONE = "none"
    MISSING = "missing"
    DUPLICATE = "duplicate"
    REGRESSION = "regression"
    FIRST_FRAME = "first_frame"

    @property
    def repaired(self) -> bool:
        return self is not RepairKind.NONE


class Policy(str, Enum):
    """How timestamp anomalies are treated once detected."""

    REPAIR = "repair"
    REPORT = "report"


@dataclass(frozen=True, slots=True)
class FrameRecord:
    """One successfully decoded frame, in decoder output order."""

    index: int
    pict_type: str
    pts: int | None
    dts: int | None
    time_base: Fraction

    def to_us(self, ticks: int | None) -> int | None:
        """Convert stream ticks to whole microseconds."""
        if ticks is None:
            return None
        return round(Fraction(ticks) * self.time_base * 1_000_000)


@dataclass(frozen=True, slots=True)
class TimelineFrame:
    """A frame after presentation-order sorting and timestamp repair."""

    frame: FrameRecord
    position: int
    original_pts: int | None
    repaired_pts: int | None
    kind: RepairKind

    @property
    def pict_type(self) -> str:
        return self.frame.pict_type

    @property
    def time_base(self) -> Fraction:
        return self.frame.time_base

    @property
    def repaired(self) -> bool:
        return self.kind.repaired

    @property
    def original_pts_us(self) -> int | None:
        return self.frame.to_us(self.original_pts)

    @property
    def repaired_pts_us(self) -> int | None:
        return self.frame.to_us(self.repaired_pts)


@dataclass(frozen=True, slots=True)
class StreamInfo:
    """Container and stream facts gathered before decoding."""

    format_name: str
    codec_name: str
    width: int
    height: int
    time_base: Fraction
    average_rate: Fraction | None
    container_frames: int | None
    container_duration_us: int | None
    start_time_us: int | None


@dataclass(frozen=True, slots=True)
class AuditResult:
    """The complete outcome for one audited input."""

    path: str
    display_path: str
    stream: StreamInfo | None
    frames: tuple[TimelineFrame, ...] = ()
    decoded_frames: int = 0
    truncated: bool = False
    max_frames: int | None = None
    policy: Policy = Policy.REPAIR
    decode_errors: tuple[str, ...] = ()
    warnings: tuple[str, ...] = ()
    error: str | None = None
    error_reason: str | None = None
    skipped_reason: str | None = None

    @property
    def ok(self) -> bool:
        return self.error is None

    @property
    def repaired_timestamps(self) -> int:
        return sum(1 for frame in self.frames if frame.kind.repaired)

    def count_type(self, pict_type: str) -> int:
        return sum(1 for frame in self.frames if frame.pict_type == pict_type)

    @property
    def i_frames(self) -> int:
        return self.count_type("I")

    @property
    def p_frames(self) -> int:
        return self.count_type("P")

    @property
    def b_frames(self) -> int:
        return self.count_type("B")

    @property
    def other_frames(self) -> int:
        return self.decoded_frames - self.i_frames - self.p_frames - self.b_frames

    @property
    def max_timestamp_jump_us(self) -> int:
        values = [
            frame.repaired_pts_us
            for frame in self.frames
            if frame.repaired_pts_us is not None
        ]
        if len(values) < 2:
            return 0
        return max(
            abs(later - earlier) for earlier, later in zip(values, values[1:])
        )

    @property
    def repair_breakdown(self) -> dict[str, int]:
        counts = {kind.value: 0 for kind in RepairKind if kind.repaired}
        for frame in self.frames:
            if frame.kind.repaired:
                counts[frame.kind.value] += 1
        return counts


def summarize_kinds(frames: Sequence[TimelineFrame]) -> dict[str, int]:
    """Count repairs by kind for tests and the JSON breakdown."""
    counts = {kind.value: 0 for kind in RepairKind if kind.repaired}
    for frame in frames:
        if frame.kind.repaired:
            counts[frame.kind.value] += 1
    return counts


@dataclass(frozen=True, slots=True)
class BatchReport:
    """Results for every input in one invocation."""

    results: tuple[AuditResult, ...] = field(default_factory=tuple)

    @property
    def audited(self) -> tuple[AuditResult, ...]:
        return tuple(result for result in self.results if result.skipped_reason is None)

    @property
    def failures(self) -> tuple[AuditResult, ...]:
        return tuple(result for result in self.results if not result.ok)

    @property
    def exit_code(self) -> int:
        return 0 if not self.failures else 1
