"""Offline render plan built from the same resolved model as the summary.

The plan describes *what* a renderer would draw and *when*; it never touches
media.  Every time is expressed in the plan frame rate, and
:mod:`cutline.report` measures the timeline span at that same rate, so the
plan and the summary can never disagree about duration.

Per track the plan lists clips in start order, the gaps between them, and the
overlap/stacking relationships against clips on the same or other tracks.  A
track that still has an overlap is flagged so the renderer knows it must
composite rather than concatenate.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from fractions import Fraction
from typing import Any

from .conflicts import Conflict, detect
from .model import Timeline
from .rational import parse_rate, round_half_away


@dataclass
class PlannedClip:
    index: int
    clip_id: str
    name: str | None
    start_frame: int
    end_frame: int
    duration_frames: int
    start: str
    end: str
    source_rate: str

    def as_dict(self) -> dict[str, Any]:
        return {
            "index": self.index,
            "clip_id": self.clip_id,
            "name": self.name,
            "start_frame": self.start_frame,
            "end_frame": self.end_frame,
            "duration_frames": self.duration_frames,
            "start": self.start,
            "end": self.end,
            "source_rate": self.source_rate,
        }


@dataclass
class PlannedGap:
    after_clip: str
    start_frame: int
    end_frame: int
    duration_frames: int

    def as_dict(self) -> dict[str, Any]:
        return {
            "after_clip": self.after_clip,
            "start_frame": self.start_frame,
            "end_frame": self.end_frame,
            "duration_frames": self.duration_frames,
        }


@dataclass
class PlannedTrack:
    track_id: str
    name: str | None
    order: int
    rate: str
    clips: list[PlannedClip] = field(default_factory=list)
    gaps: list[PlannedGap] = field(default_factory=list)
    overlaps: list[dict[str, Any]] = field(default_factory=list)

    @property
    def requires_compositing(self) -> bool:
        return bool(self.overlaps)

    def as_dict(self) -> dict[str, Any]:
        return {
            "track_id": self.track_id,
            "name": self.name,
            "order": self.order,
            "rate": self.rate,
            "clips": [clip.as_dict() for clip in self.clips],
            "gaps": [gap.as_dict() for gap in self.gaps],
            "overlaps": self.overlaps,
            "requires_compositing": self.requires_compositing,
        }


@dataclass
class RenderPlan:
    plan_rate: str
    start_frame: int
    end_frame: int
    duration_frames: int
    tracks: list[PlannedTrack] = field(default_factory=list)

    @property
    def track_count(self) -> int:
        return len(self.tracks)

    @property
    def clip_count(self) -> int:
        return sum(len(track.clips) for track in self.tracks)

    def as_dict(self) -> dict[str, Any]:
        return {
            "plan_rate": self.plan_rate,
            "start_frame": self.start_frame,
            "end_frame": self.end_frame,
            "duration_frames": self.duration_frames,
            "track_count": self.track_count,
            "clip_count": self.clip_count,
            "tracks": [track.as_dict() for track in self.tracks],
        }


def _frame(seconds: Fraction, rate: Fraction) -> int:
    return round_half_away(seconds * rate)


def default_plan_rate(timeline: Timeline) -> Fraction:
    """Use the first track's rate; fall back to 24 for an empty timeline."""
    if timeline.tracks:
        return timeline.tracks[0].rate
    return Fraction(24)


def build_plan(timeline: Timeline, plan_rate: Fraction | str | int | None = None) -> RenderPlan:
    rate = parse_rate(plan_rate) if plan_rate is not None else default_plan_rate(timeline)
    span = timeline.span
    global_start = span.start.seconds if span is not None else Fraction(0)
    global_end = span.end.seconds if span is not None else Fraction(0)
    conflicts = detect(timeline).conflicts

    planned_tracks: list[PlannedTrack] = []
    for order, track in enumerate(timeline.tracks):
        ordered = sorted(track.clips, key=lambda clip: (clip.start.seconds, clip.id))
        planned_clips = [
            PlannedClip(
                index=index,
                clip_id=clip.id,
                name=clip.name,
                start_frame=_frame(clip.start.seconds, rate),
                end_frame=_frame(clip.end.seconds, rate),
                duration_frames=_frame(clip.duration.seconds, rate),
                start=clip.start.as_seconds_text(),
                end=clip.end.as_seconds_text(),
                source_rate=str(clip.rate),
            )
            for index, clip in enumerate(ordered)
        ]
        gaps: list[PlannedGap] = []
        cursor_end = None
        previous_clip = None
        for clip in ordered:
            if cursor_end is not None and clip.start > cursor_end and previous_clip is not None:
                gaps.append(
                    PlannedGap(
                        after_clip=previous_clip.id,
                        start_frame=_frame(cursor_end.seconds, rate),
                        end_frame=_frame(clip.start.seconds, rate),
                        duration_frames=_frame((clip.start - cursor_end).seconds, rate),
                    )
                )
            cursor_end = clip.end if cursor_end is None else cursor_end.max(clip.end)
            previous_clip = clip

        planned_tracks.append(
            PlannedTrack(
                track_id=track.id,
                name=track.name,
                order=order,
                rate=str(track.rate),
                clips=planned_clips,
                gaps=gaps,
                overlaps=_describe_overlaps(track.id, conflicts),
            )
        )

    return RenderPlan(
        plan_rate=str(rate),
        start_frame=_frame(global_start, rate),
        end_frame=_frame(global_end, rate),
        duration_frames=_frame(global_end - global_start, rate),
        tracks=planned_tracks,
    )


def _describe_overlaps(track_id: str, conflicts: list[Conflict]) -> list[dict[str, Any]]:
    described: list[dict[str, Any]] = []
    for conflict in conflicts:
        if conflict.track_a == track_id:
            other_track = conflict.track_b
        elif conflict.track_b == track_id:
            other_track = conflict.track_a
        else:
            continue
        described.append(
            {
                "clip_a": conflict.clip_a,
                "track_a": conflict.track_a,
                "clip_b": conflict.clip_b,
                "track_b": conflict.track_b,
                "kind": conflict.kind.value,
                "with_track": other_track,
                "intersection": conflict.intersection.as_text(),
            }
        )
    return described
