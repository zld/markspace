"""Canonical timeline model: integer frame scale and half-open intervals.

Time values entering the pipeline (seconds, possibly fractional) are converted
to integer frames with :func:`frames_from_seconds`, which uses exact rational
arithmetic. Rounding is "nearest frame, ties toward the earlier frame", i.e.
half-open frame cells and half-frame ties round toward negative infinity.

Intervals are always left-closed/right-open ``[start, end)`` with integer
frame endpoints and ``start <= end``.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from fractions import Fraction
from typing import Iterable, Sequence

# A RationalTime-like rate; kept as an int so frame math stays exact.
FrameRate = int


class ResolutionError(ValueError):
    """Raised for invalid input (missing fields, bad IDs, unknown version...).

    The CLI maps this to exit code 2; no partial manifest is emitted.
    """


def frames_from_seconds(seconds: Fraction | int | float | str, fps: FrameRate) -> int:
    """Convert a time in seconds to an integer frame number.

    Rules:

    * exact rational arithmetic throughout (floats are first promoted to
      :class:`~fractions.Fraction`, never accumulated);
    * nearest frame; on an exact half-frame tie the *earlier* frame wins,
      i.e. round-half-toward-negative-infinity for positive and negative
      values alike (0.5 -> 0, 1.5 -> 1, -0.5 -> -1).
    """
    if not isinstance(fps, int) or fps <= 0:
        raise ResolutionError(f"frame rate must be a positive integer, got {fps!r}")
    try:
        value = Fraction(seconds)
    except (TypeError, ValueError, ZeroDivisionError) as exc:
        raise ResolutionError(f"invalid time value {seconds!r}: {exc}") from exc
    scaled = value * fps
    # Nearest frame with half-frame ties toward the *earlier* frame, i.e.
    # round-half-toward-negative-infinity for both signs:
    #   x >= 0: floor(x + 1/2)  -> 0.5 -> 0, 1.5 -> 1
    #   x <  0: ceil(x - 1/2)   -> -0.5 -> -1, -1.5 -> -2
    numerator = scaled.numerator
    denominator = scaled.denominator
    if numerator >= 0:
        return (2 * numerator + denominator - 1) // (2 * denominator)
    return -((denominator - 2 * numerator) // (2 * denominator))


@dataclass(frozen=True, slots=True)
class FrameInterval:
    """Half-open integer frame interval ``[start, end)``."""

    start: int
    end: int

    def __post_init__(self) -> None:
        if not isinstance(self.start, int) or not isinstance(self.end, int):
            raise ResolutionError("interval endpoints must be integers")
        if self.start < 0:
            raise ResolutionError(f"interval start must be >= 0, got {self.start}")
        if self.end < self.start:
            raise ResolutionError(
                f"interval end ({self.end}) precedes start ({self.start})"
            )

    @property
    def duration_frames(self) -> int:
        return self.end - self.start

    def overlaps(self, other: FrameInterval) -> bool:
        """True when the half-open intervals share at least one frame."""
        return self.start < other.end and other.start < self.end

    def intersection(self, other: FrameInterval) -> FrameInterval | None:
        start = max(self.start, other.start)
        end = min(self.end, other.end)
        if start < end:
            return FrameInterval(start, end)
        return None


@dataclass(frozen=True, slots=True)
class Clip:
    """A clip placed on a track, addressed by a stable string ID."""

    clip_id: str
    interval: FrameInterval
    priority: int
    track: str = ""

    def __post_init__(self) -> None:
        if not isinstance(self.clip_id, str) or not self.clip_id:
            raise ResolutionError("clip id must be a non-empty string")
        if not isinstance(self.priority, int):
            raise ResolutionError("priority must be an integer")
        if not isinstance(self.track, str):
            raise ResolutionError("track name must be a string")


@dataclass(frozen=True, slots=True)
class Track:
    """A named track holding an ordered list of clips."""

    name: str
    clips: tuple[Clip, ...] = ()

    @classmethod
    def from_clips(cls, name: str, clips: Iterable[Clip]) -> Track:
        return cls(name, tuple(clips))


@dataclass(frozen=True, slots=True)
class Timeline:
    """A whole timeline: frame rate plus every clip from every delivery track."""

    fps: FrameRate
    tracks: tuple[Track, ...] = ()

    @property
    def clips(self) -> tuple[Clip, ...]:
        return tuple(clip for track in self.tracks for clip in track.clips)

    @classmethod
    def from_clips(cls, fps: FrameRate, clips: Iterable[Clip]) -> Timeline:
        return cls(fps, (Track.from_clips("default", clips),))

    def validate_unique_ids(self) -> None:
        seen: set[str] = set()
        duplicates: list[str] = []
        for clip in self.clips:
            if clip.clip_id in seen and clip.clip_id not in duplicates:
                duplicates.append(clip.clip_id)
            seen.add(clip.clip_id)
        if duplicates:
            raise ResolutionError(
                "duplicate clip ids: " + ", ".join(sorted(duplicates))
            )


@dataclass(frozen=True, slots=True)
class ResolvedInterval:
    """One non-overlapping segment of the resolved timeline.

    Adjacent segments with the same owner are merged by the resolver, so two
    consecutive :class:`ResolvedInterval` values always differ in owner (or
    are separated by a gap).
    """

    interval: FrameInterval
    clip_id: str

    @property
    def start(self) -> int:
        return self.interval.start

    @property
    def end(self) -> int:
        return self.interval.end


@dataclass(frozen=True, slots=True)
class Conflict:
    """An overlapping pair of clips, counted once per unordered pair."""

    first_id: str
    second_id: str
    overlap: FrameInterval


@dataclass(frozen=True, slots=True)
class ResolutionResult:
    """Resolver output: conflicts, merged ownership intervals and coverage."""

    timeline: Timeline
    conflicts: tuple[Conflict, ...]
    intervals: tuple[ResolvedInterval, ...]
    covered_frames_before: int
    covered_frames_after: int

    @property
    def conflict_count(self) -> int:
        return len(self.conflicts)

    @property
    def duration_deviation_frames(self) -> int:
        """Covered-frame difference caused by resolution (never clip sums)."""
        return self.covered_frames_after - self.covered_frames_before


def union_covered_frames(intervals: Sequence[FrameInterval]) -> int:
    """Count distinct frames covered by the union of half-open intervals."""
    ordered = sorted(intervals, key=lambda iv: (iv.start, iv.end))
    total = 0
    cursor_start = 0
    cursor_end = 0
    have = False
    for iv in ordered:
        if not have:
            cursor_start, cursor_end = iv.start, iv.end
            have = True
        elif iv.start <= cursor_end:
            # Overlapping or touching intervals: union keeps one span.
            if iv.end > cursor_end:
                cursor_end = iv.end
        else:
            total += cursor_end - cursor_start
            cursor_start, cursor_end = iv.start, iv.end
    if have:
        total += cursor_end - cursor_start
    return total
