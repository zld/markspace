"""Overlap detection for clips sharing a track.

The scanner sorts clips by ``(timeline_start, id)`` and sweeps with an active
set keyed on ``end``.  A clip is compared only against still-active clips, so
the cost is ``O(n log n + k)`` for ``k`` reported conflicts instead of the
``O(n^2)`` of a pairwise comparison.  Semi-open spans mean that clips which
merely touch (``a.end == b.start``) never conflict, and different tracks are
never compared at all.

>>> clip_a = Clip(id="a", media_id="m", timeline_start=0, duration=10, source_in=0, source_duration=10)
>>> clip_b = Clip(id="b", media_id="m", timeline_start=5, duration=10, source_in=0, source_duration=10)
>>> conflicts = scan_track(Track(id="t", clips=(clip_a, clip_b)))
>>> conflicts[0].overlap_frames
5
"""

from __future__ import annotations

import heapq
from dataclasses import dataclass
from typing import Iterable

from .model import Clip, Sequence, Track
from .timebase import FrameSpan

# Canonical conflict ordering: by timeline start, then stable clip ids.
ConflictKey = tuple[int, str, str]


@dataclass(frozen=True, slots=True)
class Conflict:
    """A pair of overlapping clips on one track with the exact shared frames."""

    track_id: str
    first_id: str
    second_id: str
    first_span: FrameSpan
    second_span: FrameSpan
    overlap_start: int
    overlap_frames: int

    @property
    def overlap_span(self) -> FrameSpan:
        return FrameSpan(self.overlap_start, self.overlap_frames)

    @property
    def key(self) -> ConflictKey:
        return (min(self.first_span.start, self.second_span.start), self.first_id, self.second_id)

    def involves(self, clip_id: str) -> bool:
        return clip_id in (self.first_id, self.second_id)

    def to_json(self) -> dict[str, object]:
        return {
            "track_id": self.track_id,
            "first": self.first_id,
            "second": self.second_id,
            "overlap_start": self.overlap_start,
            "overlap_frames": self.overlap_frames,
        }


@dataclass(frozen=True, slots=True)
class ConflictReport:
    """Conflicts for a whole sequence, grouped in encounter order per track."""

    conflicts: tuple[Conflict, ...]

    @property
    def conflict_count(self) -> int:
        return len(self.conflicts)

    @property
    def total_overlap_frames(self) -> int:
        return sum(conflict.overlap_frames for conflict in self.conflicts)

    @property
    def track_ids(self) -> tuple[str, ...]:
        seen: list[str] = []
        for conflict in self.conflicts:
            if conflict.track_id not in seen:
                seen.append(conflict.track_id)
        return tuple(seen)

    def conflicts_in(self, track_id: str) -> tuple[Conflict, ...]:
        return tuple(c for c in self.conflicts if c.track_id == track_id)

    def to_json(self) -> dict[str, object]:
        return {
            "conflict_count": self.conflict_count,
            "total_overlap_frames": self.total_overlap_frames,
            "conflicts": [conflict.to_json() for conflict in self.conflicts],
        }


def scan_track(track: Track) -> list[Conflict]:
    """Return all overlapping pairs on one track in a deterministic order."""

    ordered = sorted(track.clips, key=lambda clip: (clip.timeline_start, clip.id))
    active: list[tuple[int, str, Clip]] = []
    found: list[Conflict] = []
    for clip in ordered:
        if clip.timeline_start < 0:  # defensive: model already enforces this
            raise ValueError("clip starts before frame 0")
        # Drop clips that end at or before this clip's start: no shared frames.
        while active and active[0][0] <= clip.timeline_start:
            heapq.heappop(active)
        for _, other_id, other in sorted(active, key=lambda item: (item[2].timeline_start, item[1])):
            overlap = other.span.intersection(clip.span)
            if overlap is None:
                continue
            first, second = sorted((other, clip), key=lambda c: (c.timeline_start, c.id))
            found.append(
                Conflict(
                    track_id=track.id,
                    first_id=first.id,
                    second_id=second.id,
                    first_span=first.span,
                    second_span=second.span,
                    overlap_start=overlap.start,
                    overlap_frames=overlap.duration,
                )
            )
        heapq.heappush(active, (clip.end, clip.id, clip))
    found.sort(key=lambda conflict: conflict.key)
    return found


def scan_sequence(sequence: Sequence) -> ConflictReport:
    """Scan every track; overlaps across different tracks are not errors."""

    conflicts: list[Conflict] = []
    for track in sequence.tracks:
        conflicts.extend(scan_track(track))
    return ConflictReport(tuple(conflicts))


def conflicting_clip_ids(report: ConflictReport) -> frozenset[str]:
    ids: set[str] = set()
    for conflict in report.conflicts:
        ids.add(conflict.first_id)
        ids.add(conflict.second_id)
    return frozenset(ids)
