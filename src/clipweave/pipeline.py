"""Pipeline: validate -> detect -> resolve -> report.

The same internal model drives both the terminal report and the OTIO
export; OTIO objects are never used as the source of truth.
"""
from __future__ import annotations

from dataclasses import dataclass
from fractions import Fraction

from .detect import Conflict, find_conflicts
from .model import DEFAULT_MAX_CLIPS, Timeline, frames, validate_timeline
from .resolve import resolve_timeline


@dataclass
class PipelineResult:
    timeline: Timeline
    resolved: Timeline
    conflicts: list[Conflict]
    report: dict


def _total_duration_frames(timeline: Timeline) -> int:
    return sum(frames(c.duration, timeline.fps) for c in timeline.clips)


def conflict_entries(conflicts: list[Conflict], fps: Fraction) -> list[dict]:
    return [
        {
            "track": c.track,
            "clip_a": c.clip_a,
            "clip_b": c.clip_b,
            "start_frame": frames(c.start, fps),
            "end_frame": frames(c.end, fps),
        }
        for c in conflicts
    ]


def interval_labels(timeline: Timeline) -> list[str]:
    labels = []
    for track in timeline.tracks:
        for clip in sorted(track.clips, key=lambda c: (c.start, c.id)):
            labels.append(
                f"{clip.id}"
                f"[{frames(clip.start, timeline.fps)},"
                f"{frames(clip.end, timeline.fps)})"
            )
    return labels


def build_report(
    timeline: Timeline, resolved: Timeline, conflicts: list[Conflict]
) -> dict:
    before = _total_duration_frames(timeline)
    after = _total_duration_frames(resolved)
    delta = after - before
    deviation = abs(delta)
    return {
        "timeline": timeline.name,
        "fps": str(timeline.fps),
        "clip_count": timeline.clip_count,
        "resolved_clip_count": resolved.clip_count,
        "conflict_count": len(conflicts),
        "conflicts": conflict_entries(conflicts, timeline.fps),
        "resolved_intervals": interval_labels(resolved),
        "total_duration_before_frames": before,
        "total_duration_after_frames": after,
        "duration_delta_frames": delta,
        "duration_deviation_frames": deviation,
        "duration_deviation_seconds": str(Fraction(deviation) / timeline.fps),
    }


def run_pipeline(
    timeline: Timeline, *, max_clips: int = DEFAULT_MAX_CLIPS
) -> PipelineResult:
    validate_timeline(timeline, max_clips=max_clips)
    conflicts = find_conflicts(timeline)
    resolved = resolve_timeline(timeline)
    report = build_report(timeline, resolved, conflicts)
    return PipelineResult(
        timeline=timeline, resolved=resolved, conflicts=conflicts, report=report
    )
