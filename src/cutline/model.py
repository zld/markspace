"""Internal timeline model: sequence, tracks, clips, source ranges and speeds.

Coordinates are integer timeline frames at the sequence frame rate.  A clip's
speed mapping is a contiguous list of :class:`RateSegment` values expressed in
clip-local timeline frames; every segment consumes an exact whole number of
source frames.

>>> clip = Clip(id="c1", media_id="m1", timeline_start=0, duration=10,
...             source_in=0, source_duration=20)
>>> clip.source_span.end
10
"""

from __future__ import annotations

from dataclasses import dataclass, replace
from fractions import Fraction
from typing import Any, Iterable

from .errors import ModelError
from .timebase import FrameRate, FrameSpan, positive_fraction, require_int

TRACK_KINDS = frozenset({"video", "audio"})


def _require_id(value: Any, field: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise ModelError(f"{field} must be a non-empty string")
    return value


@dataclass(frozen=True, slots=True)
class RateSegment:
    """A constant-speed run inside a clip, measured in clip-local frames."""

    offset: int
    duration: int
    speed: Fraction = Fraction(1)

    def __post_init__(self) -> None:
        require_int(self.offset, "rate segment offset", minimum=0)
        require_int(self.duration, "rate segment duration", minimum=1)
        speed = positive_fraction(self.speed, "rate segment speed")
        consumed = speed * self.duration
        if consumed.denominator != 1:
            raise ModelError(
                f"rate segment of {self.duration} frames at speed {speed} consumes "
                f"{consumed} source frames, not a whole frame"
            )
        object.__setattr__(self, "speed", speed)

    @property
    def end(self) -> int:
        return self.offset + self.duration

    @property
    def source_frames(self) -> int:
        return int(self.speed * self.duration)

    def to_json(self) -> dict[str, Any]:
        return {
            "offset": self.offset,
            "duration": self.duration,
            "speed": {"numerator": self.speed.numerator, "denominator": self.speed.denominator},
        }


def constant_rate(duration: int, speed: Fraction = Fraction(1)) -> tuple[RateSegment, ...]:
    return (RateSegment(0, duration, speed),)


@dataclass(frozen=True, slots=True)
class Clip:
    """A clip placed on a track with exact source mapping and fades."""

    id: str
    media_id: str
    timeline_start: int
    duration: int
    source_in: int
    source_duration: int
    rate_segments: tuple[RateSegment, ...] = ()
    fade_in: int = 0
    fade_out: int = 0
    priority: int = 0

    def __post_init__(self) -> None:
        _require_id(self.id, "clip id")
        _require_id(self.media_id, "clip media_id")
        require_int(self.timeline_start, "clip timeline_start", minimum=0)
        require_int(self.duration, "clip duration", minimum=1)
        require_int(self.source_in, "clip source_in", minimum=0)
        require_int(self.source_duration, "clip source_duration", minimum=1)
        require_int(self.fade_in, "clip fade_in", minimum=0)
        require_int(self.fade_out, "clip fade_out", minimum=0)
        require_int(self.priority, "clip priority")

        segments = tuple(self.rate_segments) or constant_rate(self.duration)
        self._validate_segments(segments)
        object.__setattr__(self, "rate_segments", segments)

        if self.source_in + self.source_frames > self.source_duration:
            raise ModelError(
                f"source range [{self.source_in}, {self.source_in + self.source_frames}) "
                f"exceeds media '{self.media_id}' duration {self.source_duration}",
                clip_id=self.id,
            )
        if self.fade_in + self.fade_out > self.duration:
            raise ModelError(
                f"fades {self.fade_in}+{self.fade_out} exceed clip duration {self.duration}",
                clip_id=self.id,
            )

    def _validate_segments(self, segments: tuple[RateSegment, ...]) -> None:
        if not segments:
            raise ModelError("clip needs at least one rate segment", clip_id=self.id)
        expected = 0
        for segment in segments:
            if not isinstance(segment, RateSegment):
                raise ModelError("rate_segments must contain RateSegment values", clip_id=self.id)
            if segment.offset != expected:
                raise ModelError(
                    f"rate segments must tile the clip: expected offset {expected}, "
                    f"got {segment.offset}",
                    clip_id=self.id,
                )
            expected = segment.end
        if expected != self.duration:
            raise ModelError(
                f"rate segments cover {expected} frames but clip duration is {self.duration}",
                clip_id=self.id,
            )

    @property
    def end(self) -> int:
        return self.timeline_start + self.duration

    @property
    def span(self) -> FrameSpan:
        return FrameSpan(self.timeline_start, self.duration)

    @property
    def source_frames(self) -> int:
        return sum(segment.source_frames for segment in self.rate_segments)

    @property
    def source_span(self) -> FrameSpan:
        return FrameSpan(self.source_in, self.source_frames)

    @property
    def speed(self) -> Fraction:
        """Exact mean speed; for a single-segment clip this is the segment speed."""

        return Fraction(self.source_frames, self.duration)

    @property
    def is_constant_speed(self) -> bool:
        return len(self.rate_segments) == 1

    def source_offset_at(self, local_frame: int) -> int:
        """Source frame offset for a clip-local timeline frame (segment lookup)."""

        require_int(local_frame, "local_frame", minimum=0)
        if local_frame >= self.duration:
            raise ModelError(
                f"local frame {local_frame} is outside clip duration {self.duration}",
                clip_id=self.id,
            )
        consumed = 0
        for segment in self.rate_segments:
            if segment.offset <= local_frame < segment.end:
                return consumed + int(segment.speed * (local_frame - segment.offset))
            consumed += segment.source_frames
        raise ModelError(f"no rate segment covers local frame {local_frame}", clip_id=self.id)

    def to_json(self) -> dict[str, Any]:
        return {
            "id": self.id,
            "media_id": self.media_id,
            "timeline_start": self.timeline_start,
            "duration": self.duration,
            "source_in": self.source_in,
            "source_duration": self.source_duration,
            "rate_segments": [segment.to_json() for segment in self.rate_segments],
            "fade_in": self.fade_in,
            "fade_out": self.fade_out,
            "priority": self.priority,
        }


@dataclass(frozen=True, slots=True)
class Track:
    """A stable typed track at a stacking level; clips are kept in original order."""

    id: str
    kind: str = "video"
    level: int = 0
    clips: tuple[Clip, ...] = ()

    def __post_init__(self) -> None:
        _require_id(self.id, "track id")
        if self.kind not in TRACK_KINDS:
            raise ModelError(f"unsupported track kind {self.kind!r}", track_id=self.id)
        require_int(self.level, "track level", minimum=0)
        clips = tuple(self.clips)
        seen: set[str] = set()
        for clip in clips:
            if not isinstance(clip, Clip):
                raise ModelError("track clips must be Clip values", track_id=self.id)
            if clip.id in seen:
                raise ModelError(f"duplicate clip id {clip.id!r}", track_id=self.id, clip_id=clip.id)
            seen.add(clip.id)
        object.__setattr__(self, "clips", clips)

    @property
    def span(self) -> FrameSpan:
        return FrameSpan(0, max((clip.end for clip in self.clips), default=0))

    def clip_by_id(self, clip_id: str) -> Clip:
        for clip in self.clips:
            if clip.id == clip_id:
                return clip
        raise ModelError(f"unknown clip {clip_id!r}", track_id=self.id, clip_id=clip_id)

    def with_clips(self, clips: Iterable[Clip]) -> "Track":
        return replace(self, clips=tuple(clips))

    def to_json(self) -> dict[str, Any]:
        return {
            "id": self.id,
            "kind": self.kind,
            "level": self.level,
            "clips": [clip.to_json() for clip in self.clips],
        }


@dataclass(frozen=True, slots=True)
class Sequence:
    """A named collection of tracks sharing one exact frame rate."""

    id: str
    frame_rate: FrameRate
    tracks: tuple[Track, ...] = ()
    name: str = "sequence"

    def __post_init__(self) -> None:
        _require_id(self.id, "sequence id")
        if not isinstance(self.frame_rate, FrameRate):
            raise ModelError("sequence frame_rate must be a FrameRate")
        tracks = tuple(self.tracks)
        track_ids: set[str] = set()
        clip_ids: set[str] = set()
        for track in tracks:
            if not isinstance(track, Track):
                raise ModelError("sequence tracks must be Track values")
            if track.id in track_ids:
                raise ModelError(f"duplicate track id {track.id!r}", track_id=track.id)
            track_ids.add(track.id)
            for clip in track.clips:
                if clip.id in clip_ids:
                    raise ModelError(
                        f"duplicate clip id {clip.id!r} across sequence",
                        track_id=track.id,
                        clip_id=clip.id,
                    )
                clip_ids.add(clip.id)
        object.__setattr__(self, "tracks", tracks)

    @property
    def duration(self) -> int:
        return max((track.span.end for track in self.tracks), default=0)

    def track_by_id(self, track_id: str) -> Track:
        for track in self.tracks:
            if track.id == track_id:
                return track
        raise ModelError(f"unknown track {track_id!r}", track_id=track_id)

    def with_tracks(self, tracks: Iterable[Track]) -> "Sequence":
        return replace(self, tracks=tuple(tracks))

    def iter_clips(self):
        for track in self.tracks:
            for clip in track.clips:
                yield track, clip

    def clip_locations(self) -> dict[str, tuple[str, int]]:
        locations: dict[str, tuple[str, int]] = {}
        for track in self.tracks:
            for index, clip in enumerate(track.clips):
                locations[clip.id] = (track.id, index)
        return locations

    def to_json(self) -> dict[str, Any]:
        return {
            "id": self.id,
            "frame_rate": self.frame_rate.to_json(),
            "tracks": [track.to_json() for track in self.tracks],
        }
