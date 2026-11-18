"""Conflict resolution strategies with an auditable decision log.

Strategies
----------
``priority``
    For every conflicting pair, the weaker clip (lower priority, then larger
    id) is trimmed against the stronger one.  A partial overlap keeps the
    larger piece (ties keep the left piece); the discarded piece is logged.
    A fully covered clip is trimmed to zero duration and then removed with an
    explicit reason -- never dropped silently.  Because trimming only ever
    shrinks a span, one deterministic pass over the initial conflict list
    leaves the timeline overlap-free.
``shift``
    Same-track only.  The later-arriving clip (by start, then id) is moved
    left so that it ends exactly where the earlier clip begins.  If the move
    would start before zero it cannot be contained, so the clip is left in
    place and the conflict is reported as unresolved.  Cross-track overlaps
    fall through to the priority pass so tracks stay stacked.
``reject``
    Change nothing; every conflict becomes an ``unresolved`` entry.

Determinism: pairs are taken in sorted intersection order and comparisons
fall back to clip ids, so repeated runs produce identical decisions.
"""

from __future__ import annotations

import bisect
from dataclasses import dataclass, field
from enum import Enum
from typing import Any

from .conflicts import Conflict, ConflictKind, detect
from .model import Clip, Timeline, Track
from .rational import RationalTime, Span


class Strategy(str, Enum):
    PRIORITY = "priority"
    SHIFT = "shift"
    REJECT = "reject"


class DecisionAction(str, Enum):
    TRIMMED = "trimmed"
    DISCARDED = "discarded"
    SHIFTED = "shifted"
    REMOVED = "removed"
    UNRESOLVED = "unresolved"


@dataclass(frozen=True)
class Decision:
    action: DecisionAction
    clip_id: str
    track_id: str
    reason: str
    before: Span | None = None
    after: Span | None = None

    def as_dict(self) -> dict[str, Any]:
        payload: dict[str, Any] = {
            "action": self.action.value,
            "clip_id": self.clip_id,
            "track_id": self.track_id,
            "reason": self.reason,
        }
        if self.before is not None:
            payload["before"] = self.before.as_text()
        if self.after is not None:
            payload["after"] = self.after.as_text()
        return payload


@dataclass
class ResolutionResult:
    timeline: Timeline
    decisions: list[Decision] = field(default_factory=list)
    unresolved: list[Conflict] = field(default_factory=list)
    strategy: Strategy = Strategy.PRIORITY
    removed: list[Decision] = field(default_factory=list)

    @property
    def has_unresolved(self) -> bool:
        return bool(self.unresolved)


def _stronger(a: Clip, b: Clip) -> int:
    """Return -1 if ``a`` wins, +1 if ``b`` wins, 0 for identical clips.

    Higher priority wins; equal priority is broken by the lexicographically
    smaller id (the larger id loses).
    """
    if a.priority != b.priority:
        return -1 if a.priority > b.priority else 1
    if a.id == b.id:
        return 0
    return -1 if a.id < b.id else 1


def _clone(timeline: Timeline) -> Timeline:
    return Timeline(
        id=timeline.id,
        name=timeline.name,
        metadata=dict(timeline.metadata),
        tracks=[
            Track(
                id=track.id,
                name=track.name,
                rate=track.rate,
                metadata=dict(track.metadata),
                clips=[clip.copy_with() for clip in track.clips],
            )
            for track in timeline.tracks
        ],
    )


def resolve(timeline: Timeline, strategy: Strategy | str) -> ResolutionResult:
    strategy = Strategy(strategy)
    if strategy is Strategy.REJECT:
        report = detect(timeline)
        decisions = [
            Decision(
                action=DecisionAction.UNRESOLVED,
                clip_id=conflict.clip_b,
                track_id=conflict.track_b,
                reason=f"reject strategy: {conflict.kind.value} with {conflict.clip_a}",
                before=conflict.intersection,
            )
            for conflict in report.conflicts
        ]
        return ResolutionResult(
            timeline=_clone(timeline),
            decisions=decisions,
            unresolved=list(report.conflicts),
            strategy=strategy,
        )

    working = _clone(timeline)
    decisions: list[Decision] = []
    if strategy is Strategy.PRIORITY:
        _resolve_priority(working, decisions)
    else:
        _resolve_shift(working, decisions)

    _drop_empty(working, decisions)

    remaining = detect(working).conflicts
    for conflict in remaining:
        decisions.append(
            Decision(
                action=DecisionAction.UNRESOLVED,
                clip_id=conflict.clip_b,
                track_id=conflict.track_b,
                reason=f"could not resolve {conflict.kind.value} with {conflict.clip_a}",
                before=conflict.intersection,
            )
        )
    return ResolutionResult(
        timeline=working,
        decisions=decisions,
        unresolved=list(remaining),
        strategy=strategy,
        removed=[d for d in decisions if d.action is DecisionAction.REMOVED],
    )


def _index(timeline: Timeline) -> dict[str, tuple[Track, Clip]]:
    return {clip.id: (track, clip) for track, clip in timeline.iter_clips()}


# ---------------------------------------------------------------------------
# priority strategy
# ---------------------------------------------------------------------------


def _surviving_piece(keep: Clip, victim: Clip) -> tuple[Span, Span | None]:
    """Return ``(surviving span, discarded span or None)`` for ``victim``.

    Only the region overlapping ``keep`` is removed.  When the victim extends
    on both sides the larger piece survives (ties keep the left piece), and
    the other piece is reported as discarded rather than silently lost.
    """
    victim_span = victim.span
    keep_span = keep.span
    if keep_span.contains(victim_span):
        return Span(victim_span.start, victim_span.start), victim_span
    left = (
        Span(victim_span.start, keep_span.start)
        if victim_span.start < keep_span.start
        else None
    )
    right = (
        Span(keep_span.end, victim_span.end)
        if keep_span.end < victim_span.end
        else None
    )
    if left is not None and right is not None:
        if left.duration.seconds >= right.duration.seconds:
            return left, right
        return right, left
    if left is not None:
        return left, None
    if right is not None:
        return right, None
    return Span(victim_span.start, victim_span.start), victim_span


def _trim_pair(
    timeline: Timeline,
    positions: dict[str, tuple[Track, Clip]],
    conflict: Conflict,
    decisions: list[Decision],
) -> None:
    """Trim the weaker clip of ``conflict`` if the pair still overlaps."""
    entry_a = positions.get(conflict.clip_a)
    entry_b = positions.get(conflict.clip_b)
    if entry_a is None or entry_b is None:
        return  # a clip was already removed by an earlier decision
    track_a, left = entry_a
    track_b, right = entry_b
    if not left.span.overlaps(right.span):
        return  # already separated by an earlier trim
    verdict = _stronger(left, right)
    if verdict == 0:
        return
    keep, victim = (left, right) if verdict < 0 else (right, left)
    victim_track = track_b if victim is right else track_a
    survivor, discarded = _surviving_piece(keep, victim)
    before = victim.span

    if survivor.is_empty:
        decisions.append(
            Decision(
                action=DecisionAction.REMOVED,
                clip_id=victim.id,
                track_id=victim_track.id,
                reason=f"fully covered by {keep.id}; trimmed to zero duration",
                before=before,
                after=Span(before.start, before.start),
            )
        )
        victim_track.clips = [clip for clip in victim_track.clips if clip.id != victim.id]
        positions.pop(victim.id, None)
        return

    victim.start = survivor.start
    victim.duration = survivor.duration
    same_track = conflict.track_a == conflict.track_b
    decisions.append(
        Decision(
            action=DecisionAction.TRIMMED,
            clip_id=victim.id,
            track_id=victim_track.id,
            reason=(
                f"{'cross-track' if not same_track else 'same-track'} overlap with {keep.id}; "
                + (
                    "lower priority"
                    if keep.priority != victim.priority
                    else "id tie-break"
                )
            ),
            before=before,
            after=survivor,
        )
    )
    if discarded is not None and not discarded.is_empty:
        decisions.append(
            Decision(
                action=DecisionAction.REMOVED,
                clip_id=victim.id,
                track_id=victim_track.id,
                reason=(
                    f"piece {discarded.as_text()['start']}..{discarded.as_text()['end']} "
                    f"of {victim.id} discarded to fit around {keep.id}"
                ),
                before=discarded,
            )
        )


def _resolve_priority(
    timeline: Timeline,
    decisions: list[Decision],
    *,
    cross_track_only: bool = False,
) -> None:
    """One deterministic pass over the initial conflict list.

    Every action shrinks a span, so no action can create a new overlap.
    Processing each original pair once is therefore sufficient for the whole
    timeline; a final :func:`detect` in :func:`resolve` verifies it.
    """
    conflicts = detect(timeline).conflicts
    if cross_track_only:
        conflicts = [
            conflict
            for conflict in conflicts
            if conflict.kind is not ConflictKind.SAME_TRACK_OVERLAP
        ]
    positions = _index(timeline)
    for conflict in conflicts:
        _trim_pair(timeline, positions, conflict, decisions)


# ---------------------------------------------------------------------------
# shift strategy
# ---------------------------------------------------------------------------


def _resolve_shift(timeline: Timeline, decisions: list[Decision]) -> None:
    """Shift later same-track clips left into the gap before their neighbour.

    Clips are visited in ``(start, id)`` order.  Earlier clips stay where they
    are; a clip that collides with an already-placed clip moves left so that
    it ends exactly where that neighbour begins, repeating if the new position
    collides again.  If no non-negative placement exists, the clip is left
    untouched and reported as unresolved instead of being dropped.  The
    placed set stays sorted and non-overlapping, so each clip costs
    ``O(k log n)`` for the ``k`` neighbours it steps past.

    Cross-track overlaps are not a shift concern (tracks are stacked), so
    only they fall through to the priority trimming pass afterwards.
    """
    for track in timeline.tracks:
        ordered = sorted(track.clips, key=lambda item: (item.start.seconds, item.id))
        placed_starts: list[Any] = []
        placed_ends: list[Any] = []
        for clip in ordered:
            duration = clip.duration.seconds
            original = clip.span
            start = clip.start.seconds
            failed = False
            while True:
                index = bisect.bisect_right(placed_ends, start)
                if index == len(placed_starts) or placed_starts[index] >= start + duration:
                    break
                start = placed_starts[index] - duration
                if start < 0:
                    failed = True
                    break
            if failed:
                # Leave the clip where it is; the final report will list it
                # as an unresolved conflict rather than dropping it.
                continue
            if start != clip.start.seconds:
                clip.start = RationalTime(start)
                decisions.append(
                    Decision(
                        action=DecisionAction.SHIFTED,
                        clip_id=clip.id,
                        track_id=track.id,
                        reason="shifted left to clear same-track overlaps",
                        before=original,
                        after=clip.span,
                    )
                )
            position = bisect.bisect_left(placed_starts, start)
            placed_starts.insert(position, start)
            placed_ends.insert(position, start + duration)

    _resolve_priority(timeline, decisions, cross_track_only=True)


def _drop_empty(timeline: Timeline, decisions: list[Decision]) -> None:
    """Remove zero-duration clips and log the removal reason."""
    for track in timeline.tracks:
        kept: list[Clip] = []
        for clip in track.clips:
            if clip.duration.seconds <= 0:
                decisions.append(
                    Decision(
                        action=DecisionAction.REMOVED,
                        clip_id=clip.id,
                        track_id=track.id,
                        reason="zero duration after trimming",
                        before=clip.span,
                    )
                )
                continue
            kept.append(clip)
        track.clips = kept
