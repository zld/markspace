"""Overlap detection and deterministic conflict resolution.

Resolution rule per covered frame:

1. the clip with the highest ``priority`` wins;
2. on equal priority, the clip whose stable ID sorts first lexicographically
   wins.

Ties are therefore fully determined by clip data, never by file or record
order. Output is a sequence of disjoint, half-open ownership intervals;
adjacent intervals owned by the same clip are merged so coverage boundaries
only appear where ownership actually changes.
"""

from __future__ import annotations

from .model import (
    Clip,
    Conflict,
    FrameInterval,
    ResolutionError,
    ResolutionResult,
    ResolvedInterval,
    Timeline,
    union_covered_frames,
)


def _clip_rank(clip: Clip) -> tuple[int, str]:
    """Sort key: larger priority first, then smaller clip ID first."""
    return (-clip.priority, clip.clip_id)


def _winner(clips: list[Clip]) -> Clip:
    return min(clips, key=_clip_rank)


def detect_conflicts(clips: list[Clip]) -> tuple[Conflict, ...]:
    """Return one :class:`Conflict` per unordered pair sharing a frame.

    Pairs are emitted in a deterministic order (winner ID first, then the
    other ID) so summaries are reproducible regardless of input ordering.
    """
    conflicts: list[Conflict] = []
    ordered = sorted(clips, key=lambda c: c.clip_id)
    for i, first in enumerate(ordered):
        for second in ordered[i + 1 :]:
            overlap = first.interval.intersection(second.interval)
            if overlap is not None:
                conflicts.append(
                    Conflict(
                        first_id=first.clip_id,
                        second_id=second.clip_id,
                        overlap=overlap,
                    )
                )
    return tuple(conflicts)


def resolve_timeline(timeline: Timeline) -> ResolutionResult:
    """Resolve every overlap in ``timeline`` into disjoint ownership spans."""
    clips = list(timeline.clips)
    timeline.validate_unique_ids()
    for clip in clips:
        if clip.interval.duration_frames <= 0:
            raise ResolutionError(
                f"clip {clip.clip_id!r} has non-positive duration "
                f"{clip.interval.duration_frames}"
            )

    conflicts = detect_conflicts(clips)
    covered_before = union_covered_frames([c.interval for c in clips])

    # Sweep line over interval endpoints. Between two consecutive endpoints
    # the set of covering clips is constant, so the winner is constant too.
    boundaries = {0}
    for clip in clips:
        boundaries.add(clip.interval.start)
        boundaries.add(clip.interval.end)
    cuts = sorted(boundaries)

    raw_spans: list[ResolvedInterval] = []
    for start, end in zip(cuts, cuts[1:]):
        if start >= end:
            continue
        window = FrameInterval(start, end)
        covering = [
            clip
            for clip in clips
            if clip.interval.overlaps(window)
        ]
        if not covering:
            continue  # uncovered gap: no ownership, no rendered frame
        owner = _winner(covering)
        raw_spans.append(ResolvedInterval(window, owner.clip_id))

    merged = _merge_adjacent(raw_spans)
    covered_after = sum(span.interval.duration_frames for span in merged)

    return ResolutionResult(
        timeline=timeline,
        conflicts=conflicts,
        intervals=tuple(merged),
        covered_frames_before=covered_before,
        covered_frames_after=covered_after,
    )


def _merge_adjacent(
    spans: list[ResolvedInterval],
) -> list[ResolvedInterval]:
    """Merge neighboring spans that touch and share the same owner."""
    merged: list[ResolvedInterval] = []
    for span in spans:
        if merged:
            previous = merged[-1]
            if (
                previous.clip_id == span.clip_id
                and previous.end == span.start
            ):
                merged[-1] = ResolvedInterval(
                    FrameInterval(previous.start, span.end),
                    span.clip_id,
                )
                continue
        merged.append(span)
    return merged
