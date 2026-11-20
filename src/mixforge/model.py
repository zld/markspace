"""Core data model and shared exceptions for mixforge.

The model layer is pure data: it describes tracks, their placement on the
target timeline and their gain automation, without any audio I/O.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path


class MixForgeError(Exception):
    """Base class for all mixforge errors."""


class ManifestError(MixForgeError):
    """Manifest content, path or value validation failed (exit code 2)."""


class AutomationError(ManifestError):
    """Gain automation curve is invalid (exit code 2)."""


class GraphError(ManifestError):
    """Mix graph has cycles or dangling dependencies (exit code 2)."""


class AudioIOError(MixForgeError):
    """Audio decoding/encoding or layout failure (exit code 3)."""


@dataclass(frozen=True)
class Keyframe:
    """A single gain automation point.

    ``time`` is in seconds, relative to the track start on the timeline.
    ``db`` is the gain in decibels at that point.
    """

    time: float
    db: float


@dataclass(frozen=True)
class Track:
    """One input track of the mix.

    ``path`` is the resolved audio file path. ``start`` is the offset in
    seconds at which the track content begins on the target timeline.
    ``keyframes`` holds the gain automation sorted by time.
    """

    track_id: str
    path: Path
    start: float
    keyframes: tuple[Keyframe, ...] = field(default_factory=tuple)


@dataclass(frozen=True)
class Project:
    """A validated set of tracks ready to be rendered."""

    tracks: tuple[Track, ...]

    def track_ids(self) -> tuple[str, ...]:
        return tuple(t.track_id for t in self.tracks)
