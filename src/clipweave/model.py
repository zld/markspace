"""Rational-time timeline model.

All times are exact :class:`fractions.Fraction` seconds; no float rounding
ever enters time computation. Intervals are half-open ``[start, end)``.
"""
from __future__ import annotations

from dataclasses import dataclass, field, replace
from fractions import Fraction
from typing import Any, Iterator, Mapping

from .errors import LimitError, ValidationError

DEFAULT_MAX_CLIPS = 50000


def as_time(value: Any) -> Fraction:
    """Coerce an int/Fraction/decimal-string to an exact Fraction.

    Floats are rejected outright: they would smuggle rounding error into
    the model.
    """
    if isinstance(value, Fraction):
        return value
    if isinstance(value, bool):
        raise ValidationError(f"unsupported time value: {value!r}")
    if isinstance(value, int):
        return Fraction(value)
    if isinstance(value, str):
        try:
            return Fraction(value)
        except (ValueError, ZeroDivisionError) as exc:
            raise ValidationError(f"invalid rational time: {value!r}") from exc
    raise ValidationError(f"unsupported time value: {value!r}")


@dataclass(frozen=True)
class Clip:
    """A clip on a track: timeline interval plus source offset and priority."""

    id: str
    start: Fraction
    duration: Fraction
    priority: int = 0
    source_start: Fraction = Fraction(0)
    metadata: Mapping[str, Any] = field(default_factory=dict)
    input_index: int = field(default=0, compare=False)

    @property
    def end(self) -> Fraction:
        return self.start + self.duration

    @property
    def source_end(self) -> Fraction:
        return self.source_start + self.duration

    def overlaps(self, other: "Clip") -> bool:
        """Strict half-open overlap; touching intervals do not overlap."""
        return self.start < other.end and other.start < self.end


@dataclass(frozen=True)
class Track:
    name: str
    clips: tuple[Clip, ...] = ()


@dataclass(frozen=True)
class Timeline:
    fps: Fraction
    tracks: tuple[Track, ...] = ()
    name: str = "timeline"

    @property
    def clips(self) -> Iterator[Clip]:
        for track in self.tracks:
            yield from track.clips

    @property
    def clip_count(self) -> int:
        return sum(len(track.clips) for track in self.tracks)


def is_frame_aligned(t: Fraction, fps: Fraction) -> bool:
    """True when ``t`` lands exactly on a frame boundary at ``fps``."""
    return (t * fps).denominator == 1


def frames(t: Fraction, fps: Fraction) -> int:
    """Exact frame count of a frame-aligned time; raises otherwise."""
    value = t * fps
    if value.denominator != 1:
        raise ValidationError(f"time {t} is not frame-aligned at fps {fps}")
    return value.numerator


def build_timeline(
    *,
    fps: Any,
    tracks: list[tuple[str, list[Clip]]],
    name: str = "timeline",
) -> Timeline:
    """Assemble a timeline, stamping clips with their global input order."""
    rate = fps if isinstance(fps, Fraction) else as_time(fps)
    built: list[Track] = []
    index = 0
    for track_name, clips in tracks:
        ordered = []
        for clip in clips:
            ordered.append(replace(clip, input_index=index))
            index += 1
        built.append(Track(name=track_name, clips=tuple(ordered)))
    return Timeline(fps=rate, tracks=tuple(built), name=name)


def validate_timeline(
    timeline: Timeline, *, max_clips: int = DEFAULT_MAX_CLIPS
) -> Timeline:
    """Enforce model invariants; returns the timeline unchanged on success."""
    if timeline.fps <= 0:
        raise ValidationError(f"frame rate must be positive, got {timeline.fps}")
    if timeline.clip_count > max_clips:
        raise LimitError(
            f"clip count {timeline.clip_count} exceeds limit {max_clips}"
        )
    seen: set[str] = set()
    for track in timeline.tracks:
        for clip in track.clips:
            if not isinstance(clip.id, str) or not clip.id:
                raise ValidationError("clip id must be a non-empty string")
            if clip.id in seen:
                raise ValidationError(
                    f"duplicate clip id: {clip.id}", clip_id=clip.id
                )
            seen.add(clip.id)
            if clip.duration < 0:
                raise ValidationError("negative duration", clip_id=clip.id)
            if clip.source_start < 0:
                raise ValidationError("negative source offset", clip_id=clip.id)
            for label, t in (
                ("start", clip.start),
                ("duration", clip.duration),
                ("source_start", clip.source_start),
            ):
                if not isinstance(t, Fraction):
                    raise ValidationError(
                        f"{label} is not an exact rational: {t!r}",
                        clip_id=clip.id,
                    )
                if not is_frame_aligned(t, timeline.fps):
                    raise ValidationError(
                        f"{label} {t} is not frame-aligned at fps {timeline.fps}",
                        clip_id=clip.id,
                    )
    return timeline
