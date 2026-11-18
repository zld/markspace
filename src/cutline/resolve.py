"""Conflict resolution: strategy surface and rejection.

``priority`` trims the weaker clip of each colliding pair (lower priority,
then larger id), keeps the larger surviving piece, logs discarded pieces and
removes fully covered clips with an explicit reason.

``reject`` changes nothing and marks every conflict unresolved.

Determinism: pairs are processed in sorted intersection order and comparisons
fall back to clip ids, so repeated runs produce identical decisions.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum
from typing import Any

from .conflicts import Conflict, detect
from .model import Clip, Timeline, Track
from .rational import Span


class Strategy(str, Enum):
    PRIORITY = "priority"
    REJECT = "reject"


class DecisionAction(str, Enum):
    TRIMMED = "trimmed"
    DISCARDED = "discarded"
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
    _resolve_priority(working, decisions)

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


def resolve(timeline: Timeline, strategy: Strategy | str) -> ResolutionResult:
    """Report every conflict as unresolved (trimming follows)."""
    strategy = Strategy(strategy)
    working = _clone(timeline)
    conflicts = detect(working).conflicts
    decisions = [
        Decision(
            action=DecisionAction.UNRESOLVED,
            clip_id=conflict.clip_b,
            track_id=conflict.track_b,
            reason=f"{strategy.value} strategy cannot resolve {conflict.kind.value} with {conflict.clip_a}",
            before=conflict.intersection,
        )
        for conflict in conflicts
    ]
    return ResolutionResult(
        timeline=working,
        decisions=decisions,
        unresolved=list(conflicts),
        strategy=strategy,
    )
