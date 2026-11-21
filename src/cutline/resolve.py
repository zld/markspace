"""Conflict detection and resolution on exclusive tracks.

Conflicts are pairs of clips on the same exclusive track whose half-open
intervals [start, end) intersect.  Abutting clips (a.end == b.start) are
not conflicts.  Layered tracks permit overlap and are left untouched.

Resolution orders clips by (start, -priority, clip_id) and shifts each
later clip right to the nearest free position, preserving duration and
source range.  A single sweep handles continuous overlap chains, and
re-running on resolved output is a fixed point.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from fractions import Fraction

from .model import Clip, Gap, Timeline, Track


@dataclass(frozen=True)
class Conflict:
    track: str
    clip_a: str
    clip_b: str
    overlap_start: Fraction
    overlap_end: Fraction


@dataclass(frozen=True)
class Move:
    track: str
    clip_id: str
    old_start: Fraction
    new_start: Fraction


@dataclass
class ResolveResult:
    timeline: Timeline
    conflicts: list[Conflict]
    moves: list[Move]
    duration_before: Fraction   # seconds
    duration_after: Fraction    # seconds

    @property
    def duration_delta_seconds(self) -> Fraction:
        return self.duration_after - self.duration_before

    def duration_delta_frames(self) -> Fraction:
        return self.duration_delta_seconds * self.timeline.rate


def _overlaps(a_start: Fraction, a_end: Fraction, b_start: Fraction, b_end: Fraction) -> bool:
    return a_start < b_end and b_start < a_end


def find_conflicts(timeline: Timeline) -> list[Conflict]:
    """O(n log n) sweep per exclusive track; reports all overlapping pairs."""
    conflicts: list[Conflict] = []
    for track in timeline.tracks:
        if track.mode != "exclusive":
            continue
        clips = sorted(track.clips(), key=lambda c: (c.start, c.end, c.clip_id))
        # Active list of clips whose end is beyond the current start.
        active: list[Clip] = []
        for clip in clips:
            active = [c for c in active if c.end > clip.start]
            for other in active:
                conflicts.append(
                    Conflict(
                        track=track.name,
                        clip_a=other.clip_id,
                        clip_b=clip.clip_id,
                        overlap_start=max(other.start, clip.start),
                        overlap_end=min(other.end, clip.end),
                    )
                )
            active.append(clip)
    return conflicts


def _resolve_track(track: Track) -> tuple[Track, list[Move]]:
    clips = sorted(track.clips(), key=lambda c: (c.start, -c.priority, c.clip_id))
    gaps = [it for it in track.items if isinstance(it, Gap)]
    moves: list[Move] = []
    cursor: Fraction | None = None
    placed: list[Clip] = []
    for clip in clips:
        start = clip.start
        if cursor is not None and start < cursor:
            start = cursor
        if start != clip.start:
            moves.append(
                Move(track=track.name, clip_id=clip.clip_id,
                     old_start=clip.start, new_start=start)
            )
            clip = clip.moved_to(start)
        cursor = clip.end
        placed.append(clip)
    items = sorted([*placed, *gaps], key=lambda it: (it.start, getattr(it, "clip_id", "")))
    return Track(name=track.name, kind=track.kind, mode=track.mode, items=items), moves


def resolve(timeline: Timeline) -> ResolveResult:
    """Return a resolved copy of the timeline plus the move record."""
    conflicts = find_conflicts(timeline)
    before = timeline.duration_seconds
    new_tracks: list[Track] = []
    moves: list[Move] = []
    for track in timeline.tracks:
        if track.mode == "exclusive":
            new_track, track_moves = _resolve_track(track)
            moves.extend(track_moves)
        else:
            new_track = Track(name=track.name, kind=track.kind,
                              mode=track.mode, items=list(track.items))
        new_tracks.append(new_track)
    resolved = Timeline(name=timeline.name, rate=timeline.rate, tracks=new_tracks)
    return ResolveResult(
        timeline=resolved,
        conflicts=conflicts,
        moves=moves,
        duration_before=before,
        duration_after=resolved.duration_seconds,
    )
