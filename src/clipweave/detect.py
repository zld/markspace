"""Same-track overlap detection.

Only clips on the same track can conflict; touching half-open intervals
(``a.end == b.start``) are not conflicts. Different tracks run in parallel
and never interfere.
"""
from __future__ import annotations

from dataclasses import dataclass
from fractions import Fraction

from .model import Timeline


@dataclass(frozen=True)
class Conflict:
    track: str
    clip_a: str
    clip_b: str
    start: Fraction
    end: Fraction


def find_conflicts(timeline: Timeline) -> list[Conflict]:
    """Return every overlapping clip pair, in deterministic order.

    Sweep-line per track: clips sorted by (start, input order, id); each
    clip is checked against the still-open clips seen before it. The pair
    count is exact.
    """
    conflicts: list[Conflict] = []
    for track in timeline.tracks:
        ordered = sorted(
            track.clips, key=lambda c: (c.start, c.input_index, c.id)
        )
        active: list = []
        for clip in ordered:
            active = [c for c in active if c.end > clip.start]
            for other in active:
                conflicts.append(
                    Conflict(
                        track=track.name,
                        clip_a=other.id,
                        clip_b=clip.id,
                        start=max(other.start, clip.start),
                        end=min(other.end, clip.end),
                    )
                )
            active.append(clip)
    return conflicts
