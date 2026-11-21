"""Core timeline model with exact rational arithmetic.

All times are ``fractions.Fraction`` values measured in frames at the
owning timeline's rate.  Source in/out points are frames at the media's
own rate.  No float accumulation is used anywhere in the package.
"""

from __future__ import annotations

from dataclasses import dataclass, field, replace
from fractions import Fraction
from typing import Iterator, Optional, Union

MAX_CLIPS_PER_TIMELINE = 10_000

TRACK_KINDS = ("video", "audio")
TRACK_MODES = ("exclusive", "layered")


class ModelError(ValueError):
    """Raised when a timeline violates model invariants."""


def as_fraction(value: Union[int, Fraction]) -> Fraction:
    if isinstance(value, Fraction):
        return value
    if isinstance(value, bool):
        raise ModelError(f"boolean is not a valid time value: {value!r}")
    if isinstance(value, int):
        return Fraction(value)
    raise ModelError(f"time value must be int or Fraction, got {type(value).__name__}")


@dataclass(frozen=True)
class MediaRef:
    """Reference to source media.  ``duration`` is the available source
    length in frames at ``rate``; ``None`` means unknown."""

    name: str
    rate: Fraction
    uri: Optional[str] = None
    duration: Optional[Fraction] = None

    def __post_init__(self) -> None:
        object.__setattr__(self, "rate", as_fraction(self.rate))
        if self.rate <= 0:
            raise ModelError(f"media {self.name!r}: rate must be positive, got {self.rate}")
        if self.duration is not None:
            object.__setattr__(self, "duration", as_fraction(self.duration))
            if self.duration < 0:
                raise ModelError(f"media {self.name!r}: negative duration {self.duration}")


@dataclass(frozen=True)
class Clip:
    clip_id: str
    name: str
    start: Fraction          # timeline frames
    duration: Fraction       # timeline frames, >= 0
    source_in: Fraction      # frames at media rate
    source_out: Fraction     # frames at media rate
    media: MediaRef
    priority: int = 0

    def __post_init__(self) -> None:
        for attr in ("start", "duration", "source_in", "source_out"):
            object.__setattr__(self, attr, as_fraction(getattr(self, attr)))

    @property
    def end(self) -> Fraction:
        return self.start + self.duration

    def moved_to(self, new_start: Fraction) -> "Clip":
        return replace(self, start=new_start)


@dataclass(frozen=True)
class Gap:
    start: Fraction
    duration: Fraction

    def __post_init__(self) -> None:
        object.__setattr__(self, "start", as_fraction(self.start))
        object.__setattr__(self, "duration", as_fraction(self.duration))

    @property
    def end(self) -> Fraction:
        return self.start + self.duration


Item = Union[Clip, Gap]


@dataclass
class Track:
    name: str
    kind: str
    mode: str
    items: list[Item] = field(default_factory=list)

    def clips(self) -> list[Clip]:
        return [it for it in self.items if isinstance(it, Clip)]

    @property
    def end(self) -> Fraction:
        ends = [it.end for it in self.items]
        return max(ends) if ends else Fraction(0)


@dataclass
class Timeline:
    name: str
    rate: Fraction           # project frames per second
    tracks: list[Track] = field(default_factory=list)

    def __post_init__(self) -> None:
        self.rate = as_fraction(self.rate)

    @property
    def end(self) -> Fraction:
        ends = [t.end for t in self.tracks]
        return max(ends) if ends else Fraction(0)

    @property
    def duration_seconds(self) -> Fraction:
        return self.end / self.rate

    def clip_count(self) -> int:
        return sum(len(t.clips()) for t in self.tracks)

    def iter_clips(self) -> Iterator[tuple[Track, Clip]]:
        for track in self.tracks:
            for clip in track.clips():
                yield track, clip


def validate(timeline: Timeline) -> Timeline:
    """Check model invariants; raises ModelError with context."""
    if timeline.rate <= 0:
        raise ModelError(f"timeline {timeline.name!r}: rate must be positive, got {timeline.rate}")
    total = timeline.clip_count()
    if total > MAX_CLIPS_PER_TIMELINE:
        raise ModelError(
            f"timeline {timeline.name!r}: {total} clips exceeds limit "
            f"of {MAX_CLIPS_PER_TIMELINE}"
        )
    seen_ids: set[str] = set()
    for track in timeline.tracks:
        if track.kind not in TRACK_KINDS:
            raise ModelError(f"track {track.name!r}: unknown kind {track.kind!r}")
        if track.mode not in TRACK_MODES:
            raise ModelError(f"track {track.name!r}: unknown mode {track.mode!r}")
        for item in track.items:
            if item.duration < 0:
                raise ModelError(
                    f"track {track.name!r} item {item!r}: negative duration {item.duration}"
                )
            if not isinstance(item, Clip):
                continue
            if item.clip_id in seen_ids:
                raise ModelError(f"duplicate clip id {item.clip_id!r}")
            seen_ids.add(item.clip_id)
            if item.source_out < item.source_in:
                raise ModelError(
                    f"clip {item.clip_id!r}: source_out {item.source_out} "
                    f"< source_in {item.source_in}"
                )
            media = item.media
            if media.duration is not None and item.source_out > media.duration:
                raise ModelError(
                    f"clip {item.clip_id!r}: source_out {item.source_out} exceeds "
                    f"media {media.name!r} duration {media.duration}"
                )
    return timeline
