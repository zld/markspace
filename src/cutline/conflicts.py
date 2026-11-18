"""Conflict detection: same-track overlaps, cross-track overlap, adjacency.

A conflict is always a *proper* overlap of half-open spans.  Spans that merely
touch (``a.end == b.start``) are reported as adjacency, never as conflicts.

Detection is one global sweep over all clips ordered by ``(start, track id,
clip id)``.  A clip is compared only with clips still open at its start, and a
min-heap drops clips as soon as they end, so typical timelines run in
``O(n log n)``.  Output is sorted by intersection start and clip identifiers,
which makes repeated runs byte-identical.
"""

from __future__ import annotations

import heapq
from dataclasses import dataclass
from enum import Enum
from typing import Any

from .model import Clip, Timeline
from .rational import Span


class ConflictKind(str, Enum):
    SAME_TRACK_OVERLAP = "same_track_overlap"
    CROSS_TRACK_OVERLAP = "cross_track_overlap"
    ADJACENT = "adjacent"


@dataclass(frozen=True)
class Conflict:
    kind: ConflictKind
    clip_a: str
    track_a: str
    clip_b: str
    track_b: str
    intersection: Span
    relation: str = "partial"

    @property
    def is_conflict(self) -> bool:
        return self.kind is not ConflictKind.ADJACENT

    @property
    def sort_key(self) -> tuple:
        return (
            self.intersection.start.seconds,
            self.intersection.end.seconds,
            self.clip_a,
            self.clip_b,
            self.track_a,
            self.track_b,
        )

    def as_dict(self) -> dict[str, Any]:
        return {
            "kind": self.kind.value,
            "clip_a": self.clip_a,
            "track_a": self.track_a,
            "clip_b": self.clip_b,
            "track_b": self.track_b,
            "relation": self.relation,
            "intersection": self.intersection.as_text(),
        }


@dataclass(frozen=True)
class ConflictReport:
    conflicts: list[Conflict]
    adjacent: list[Conflict]

    def count(self, kind: ConflictKind | None = None) -> int:
        if kind is None:
            return len(self.conflicts)
        return sum(1 for conflict in self.conflicts if conflict.kind is kind)


def _relation(other: Span, current: Span) -> str:
    if other.contains(current):
        return "covered"
    if current.contains(other):
        return "covers"
    return "partial"


def detect(timeline: Timeline, *, include_adjacent: bool = True) -> ConflictReport:
    """Detect every conflict in ``timeline`` with a stable output order."""
    entries: list[tuple[Clip, str]] = [
        (clip, track.id) for track in timeline.tracks for clip in track.clips
    ]
    entries.sort(key=lambda item: (item[0].start.seconds, item[1], item[0].id))

    conflicts: list[Conflict] = []
    adjacent: list[Conflict] = []
    active: dict[tuple[str, str], tuple[Clip, str]] = {}
    heap: list[tuple[Any, int, tuple[str, str]]] = []
    counter = 0

    for clip, track_id in entries:
        while heap and heap[0][0] < clip.start:
            _, _, key = heapq.heappop(heap)
            # Lazy deletion: the key may already be gone after a duplicate push.
            active.pop(key, None)
        for other, other_track_id in active.values():
            same_track = other_track_id == track_id
            if other.end == clip.start or clip.end == other.start:
                adjacent.append(
                    Conflict(
                        kind=ConflictKind.ADJACENT,
                        clip_a=other.id,
                        track_a=other_track_id,
                        clip_b=clip.id,
                        track_b=track_id,
                        intersection=Span(clip.start, clip.start),
                    )
                )
                continue
            overlap = other.span.intersection(clip.span)
            if overlap is None:
                continue
            conflicts.append(
                Conflict(
                    kind=(
                        ConflictKind.SAME_TRACK_OVERLAP
                        if same_track
                        else ConflictKind.CROSS_TRACK_OVERLAP
                    ),
                    clip_a=other.id,
                    track_a=other_track_id,
                    clip_b=clip.id,
                    track_b=track_id,
                    intersection=overlap,
                    relation=_relation(other.span, clip.span),
                )
            )
        key = (track_id, clip.id)
        active[key] = (clip, track_id)
        heapq.heappush(heap, (clip.end, counter, key))
        counter += 1

    conflicts.sort(key=lambda item: item.sort_key)
    adjacent.sort(key=lambda item: item.sort_key)
    if not include_adjacent:
        adjacent = []
    return ConflictReport(conflicts=conflicts, adjacent=adjacent)


def detect_overlaps(timeline: Timeline) -> list[Conflict]:
    return detect(timeline).conflicts
