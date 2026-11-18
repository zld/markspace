"""In-memory timeline model: tracks, clips, and validated exact spans.

Priority convention: a **higher** integer is stronger.  Ties are broken by
the clip id in ascending lexicographic order, so the winner never depends on
input ordering or on Python's object hashing.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from fractions import Fraction
from typing import Any, Iterable

from .rational import RationalTime, Span, TimeError, parse_rate, parse_seconds


class ModelError(ValueError):
    """One or more validation problems, each tied to a data path."""

    def __init__(self, issues: Iterable["ModelIssue"] | str) -> None:
        if isinstance(issues, str):
            issues = [ModelIssue("<model>", issues)]
        self.issues = list(issues)
        joined = "; ".join(f"{issue.path}: {issue.message}" for issue in self.issues)
        super().__init__(joined)


@dataclass(frozen=True)
class ModelIssue:
    path: str
    message: str

    def as_dict(self) -> dict[str, str]:
        return {"path": self.path, "message": self.message}


@dataclass
class Clip:
    id: str
    start: RationalTime
    duration: RationalTime
    rate: Fraction
    name: str | None = None
    priority: int = 0
    metadata: dict[str, Any] = field(default_factory=dict)

    @property
    def span(self) -> Span:
        return Span.from_start_duration(self.start, self.duration)

    @property
    def end(self) -> RationalTime:
        return self.start + self.duration

    def copy_with(self, **changes: Any) -> "Clip":
        data: dict[str, Any] = {
            "id": self.id,
            "start": self.start,
            "duration": self.duration,
            "rate": self.rate,
            "name": self.name,
            "priority": self.priority,
            "metadata": dict(self.metadata),
        }
        data.update(changes)
        return Clip(**data)


@dataclass
class Track:
    id: str
    clips: list[Clip] = field(default_factory=list)
    name: str | None = None
    rate: Fraction = Fraction(24)
    metadata: dict[str, Any] = field(default_factory=dict)

    @property
    def span(self) -> Span | None:
        if not self.clips:
            return None
        first = min(clip.start for clip in self.clips)
        last = max(clip.end for clip in self.clips)
        return Span(first, last)


@dataclass
class Timeline:
    id: str = "timeline"
    name: str | None = None
    tracks: list[Track] = field(default_factory=list)
    metadata: dict[str, Any] = field(default_factory=dict)

    @property
    def span(self) -> Span | None:
        spans = [track.span for track in self.tracks if track.span is not None]
        if not spans:
            return None
        return Span(min(span.start for span in spans), max(span.end for span in spans))

    @property
    def clip_count(self) -> int:
        return sum(len(track.clips) for track in self.tracks)

    def iter_clips(self) -> Iterable[tuple[Track, Clip]]:
        for track in self.tracks:
            for clip in track.clips:
                yield track, clip


def validate_timeline(timeline: Timeline) -> None:
    """Raise :class:`ModelError` listing every locating problem found."""
    issues: list[ModelIssue] = []
    track_ids: set[str] = set()
    clip_ids: set[str] = set()

    if not timeline.id:
        issues.append(ModelIssue("timeline.id", "must not be empty"))

    for t_index, track in enumerate(timeline.tracks):
        t_path = f"tracks[{t_index}]"
        if not track.id:
            issues.append(ModelIssue(f"{t_path}.id", "must not be empty"))
        elif track.id in track_ids:
            issues.append(ModelIssue(f"{t_path}.id", f"duplicate track id {track.id!r}"))
        track_ids.add(track.id)
        if track.rate <= 0:
            issues.append(ModelIssue(f"{t_path}.rate", "must be positive"))
        for c_index, clip in enumerate(track.clips):
            validate_clip(clip, f"{t_path}.clips[{c_index}]", clip_ids, issues)

    if issues:
        raise ModelError(issues)


def validate_clip(clip: Clip, path: str, clip_ids: set[str], issues: list[ModelIssue]) -> None:
    if not clip.id:
        issues.append(ModelIssue(f"{path}.id", "must not be empty"))
    elif clip.id in clip_ids:
        issues.append(ModelIssue(f"{path}.id", f"duplicate clip id {clip.id!r}"))
    else:
        clip_ids.add(clip.id)
    if clip.start.seconds < 0:
        issues.append(ModelIssue(f"{path}.start", f"negative start {clip.start.as_seconds_text()}"))
    if clip.duration.seconds <= 0:
        issues.append(
            ModelIssue(
                f"{path}.duration",
                f"duration must be positive, got {clip.duration.as_seconds_text()}",
            )
        )
    if clip.rate <= 0:
        issues.append(ModelIssue(f"{path}.rate", "must be positive"))
    if clip.priority < 0:
        issues.append(ModelIssue(f"{path}.priority", "must be zero or greater"))


def build_timeline(data: dict[str, Any]) -> Timeline:
    """Build and validate a timeline from a plain exchange dict."""
    issues: list[ModelIssue] = []

    def fail(path: str, message: str) -> None:
        issues.append(ModelIssue(path, message))

    def as_time(value: Any, path: str) -> RationalTime | None:
        if value is None:
            fail(path, "required field is missing")
            return None
        try:
            return RationalTime(parse_seconds(value, path))
        except TimeError as exc:
            fail(path, str(exc))
            return None

    raw_tracks = data.get("tracks")
    if not isinstance(raw_tracks, list):
        fail("tracks", "must be a list")
        raise ModelError(issues)

    timeline = Timeline(
        id=str(data.get("id", "timeline")),
        name=data.get("name"),
        metadata=dict(data.get("metadata") or {}),
    )
    for t_index, raw_track in enumerate(raw_tracks):
        t_path = f"tracks[{t_index}]"
        if not isinstance(raw_track, dict):
            fail(t_path, "must be an object")
            continue
        try:
            rate = parse_rate(raw_track.get("rate", 24), f"{t_path}.rate")
        except TimeError as exc:
            fail(f"{t_path}.rate", str(exc))
            rate = Fraction(24)
        track = Track(
            id=str(raw_track.get("id", f"track-{t_index}")),
            name=raw_track.get("name"),
            rate=rate,
            metadata=dict(raw_track.get("metadata") or {}),
        )
        raw_clips = raw_track.get("clips", [])
        if not isinstance(raw_clips, list):
            fail(f"{t_path}.clips", "must be a list")
            continue
        for c_index, raw_clip in enumerate(raw_clips):
            c_path = f"{t_path}.clips[{c_index}]"
            if not isinstance(raw_clip, dict):
                fail(c_path, "must be an object")
                continue
            start = as_time(raw_clip.get("start"), f"{c_path}.start")
            duration = as_time(raw_clip.get("duration"), f"{c_path}.duration")
            try:
                clip_rate = parse_rate(raw_clip.get("rate", rate), f"{c_path}.rate")
            except TimeError as exc:
                fail(f"{c_path}.rate", str(exc))
                clip_rate = rate
            priority = raw_clip.get("priority", 0)
            if isinstance(priority, bool) or not isinstance(priority, int):
                fail(f"{c_path}.priority", "must be an integer")
                priority = 0
            if start is None or duration is None:
                continue
            try:
                span = Span.from_start_duration(start, duration)
            except TimeError as exc:
                fail(f"{c_path}.duration", str(exc))
                continue
            track.clips.append(
                Clip(
                    id=str(raw_clip.get("id", f"{track.id}-clip-{c_index}")),
                    name=raw_clip.get("name"),
                    start=span.start,
                    duration=span.duration,
                    rate=clip_rate,
                    priority=priority,
                    metadata=dict(raw_clip.get("metadata") or {}),
                )
            )
        timeline.tracks.append(track)

    if issues:
        raise ModelError(issues)
    validate_timeline(timeline)
    return timeline
