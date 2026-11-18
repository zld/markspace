"""Summary assembly: conflict counts, resolved intervals, duration deltas.

Every number here is computed on one time base: the plan's frame rate.  The
plan itself is produced by :mod:`cutline.render` from the same resolved model,
so ``render_track_count`` and the conflict summary can never disagree about
what will be rendered.

Exit semantics (mirrored by the CLI):

* ``0`` -- clean result within tolerance;
* ``3`` -- unresolved conflicts remain, or the duration delta exceeds the
  tolerance.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

from .conflicts import Conflict, ConflictKind, detect
from .model import Timeline
from .rational import Span, parse_rate, round_half_away

DEFAULT_TOLERANCE_FRAMES = 0


@dataclass
class IntervalRecord:
    track: str
    clip: str
    start: str
    end: str

    def as_dict(self) -> dict[str, str]:
        return {"track": self.track, "clip": self.clip, "start": self.start, "end": self.end}


@dataclass
class AnalyzeSummary:
    conflict_count: int
    conflict_kinds: dict[str, int]
    resolved_intervals: list[IntervalRecord]
    duration_before_frames: int
    duration_after_frames: int
    duration_delta_frames: int
    tolerance_frames: int
    within_tolerance: bool
    render_track_count: int
    exchange_version: str
    strategy: str
    track_count: int
    clip_count_before: int
    clip_count_after: int
    removed_clips: list[str] = field(default_factory=list)
    unresolved: list[dict[str, Any]] = field(default_factory=list)
    decisions: list[dict[str, Any]] = field(default_factory=list)
    conflicts: list[dict[str, Any]] = field(default_factory=list)
    adjacent: list[dict[str, Any]] = field(default_factory=list)
    warnings: list[str] = field(default_factory=list)
    status: str = "ok"

    @property
    def exit_code(self) -> int:
        if self.unresolved or not self.within_tolerance:
            return 3
        return 0

    def as_dict(self) -> dict[str, Any]:
        return {
            "status": self.status,
            "exchange_version": self.exchange_version,
            "strategy": self.strategy,
            "conflict_count": self.conflict_count,
            "conflict_kinds": self.conflict_kinds,
            "resolved_intervals": [item.as_dict() for item in self.resolved_intervals],
            "duration_before_frames": self.duration_before_frames,
            "duration_after_frames": self.duration_after_frames,
            "duration_delta_frames": self.duration_delta_frames,
            "tolerance_frames": self.tolerance_frames,
            "within_tolerance": self.within_tolerance,
            "render_track_count": self.render_track_count,
            "track_count": self.track_count,
            "clip_count_before": self.clip_count_before,
            "clip_count_after": self.clip_count_after,
            "removed_clips": self.removed_clips,
            "unresolved": self.unresolved,
            "decisions": self.decisions,
            "conflicts": self.conflicts,
            "adjacent": self.adjacent,
            "warnings": self.warnings,
        }


def span_frames(span: Span | None, rate: Any) -> int:
    """Measure a span's duration in frames at ``rate`` (exact, then rounded)."""
    frame_rate = parse_rate(rate)
    if span is None:
        return 0
    return round_half_away(span.duration.seconds * frame_rate)


def _all_intervals(timeline: Timeline) -> list[IntervalRecord]:
    records: list[IntervalRecord] = []
    for track in timeline.tracks:
        for clip in sorted(track.clips, key=lambda item: (item.start.seconds, item.id)):
            records.append(
                IntervalRecord(
                    track=track.id,
                    clip=clip.id,
                    start=clip.start.as_seconds_text(),
                    end=clip.end.as_seconds_text(),
                )
            )
    return records


def build_summary(
    before: Timeline,
    after: Timeline,
    *,
    duration_after_frames: int,
    exchange_version: str,
    strategy: str,
    tolerance_frames: int = DEFAULT_TOLERANCE_FRAMES,
    rate: Any,
    decisions: list[dict[str, Any]] | None = None,
    unresolved: list[Conflict] | None = None,
    removed_clips: list[str] | None = None,
    warnings: list[str] | None = None,
) -> AnalyzeSummary:
    """Assemble the JSON summary from the before/after models and the plan.

    ``duration_after_frames`` must come from the render plan so that the
    summary and the plan share one measurement.
    """
    before_report = detect(before)
    after_report = detect(after)
    duration_before = span_frames(before.span, rate)
    delta = duration_after_frames - duration_before
    unresolved_items = list(unresolved if unresolved is not None else after_report.conflicts)
    within = abs(delta) <= tolerance_frames
    summary = AnalyzeSummary(
        conflict_count=len(before_report.conflicts),
        conflict_kinds={
            kind.value: before_report.count(kind)
            for kind in ConflictKind
            if kind is not ConflictKind.ADJACENT
        },
        resolved_intervals=_all_intervals(after),
        duration_before_frames=duration_before,
        duration_after_frames=duration_after_frames,
        duration_delta_frames=delta,
        tolerance_frames=tolerance_frames,
        within_tolerance=within,
        render_track_count=len(after.tracks),
        exchange_version=exchange_version,
        strategy=strategy,
        track_count=len(after.tracks),
        clip_count_before=before.clip_count,
        clip_count_after=after.clip_count,
        removed_clips=sorted(set(removed_clips or [])),
        unresolved=[conflict.as_dict() for conflict in unresolved_items],
        decisions=list(decisions or []),
        conflicts=[conflict.as_dict() for conflict in before_report.conflicts],
        adjacent=[conflict.as_dict() for conflict in before_report.adjacent],
        warnings=list(warnings or []),
    )
    if summary.unresolved:
        summary.status = "unresolved_conflicts"
    elif not summary.within_tolerance:
        summary.status = "duration_out_of_tolerance"
    return summary
