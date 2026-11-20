"""Audio cue to video frame mapping.

Audio cues are exact integer sample positions at an integer sample rate. A cue
is mapped to the *nearest* video frame start on the timeline clock; when the
cue sits exactly halfway between two frames the earlier frame wins. The
signed error is ``cue_time - frame_pts`` in milliseconds (positive means the
cue lands late relative to its frame), computed with exact rationals and only
converted to float at the end.
"""

from __future__ import annotations

from dataclasses import dataclass
from fractions import Fraction
from typing import Iterable, Sequence

from .clock import FrameClock
from .schema import AudioEvent

_MS_PER_SECOND = 1000


@dataclass(frozen=True)
class CueAlignment:
    """Mapping of one :class:`AudioEvent` onto the frame grid."""

    label: str
    sample: int
    sample_rate: int
    cue_seconds: Fraction
    frame_index: int
    frame_pts: Fraction
    error_ms: float

    @property
    def error_milliseconds(self) -> Fraction:
        """Exact signed error in milliseconds before float conversion."""
        return (self.cue_seconds - self.frame_pts) * _MS_PER_SECOND


@dataclass(frozen=True)
class AudioSummary:
    """Aggregate alignment result for a whole timeline."""

    alignments: tuple[CueAlignment, ...]
    max_abs_error_ms: float

    @property
    def event_count(self) -> int:
        return len(self.alignments)

    def errors_by_frame(self) -> dict[int, list[float]]:
        """Group signed per-cue errors by frame index, preserving cue order."""
        grouped: dict[int, list[float]] = {}
        for alignment in self.alignments:
            grouped.setdefault(alignment.frame_index, []).append(alignment.error_ms)
        return grouped


def nearest_frame(clock: FrameClock, timestamp: Fraction) -> int:
    """Nearest frame whose start is closest to ``timestamp``; ties go earlier.

    Timestamps before the first frame clamp to frame 0. Timestamps past the
    last frame clamp to the last frame so a cue outside the timeline still
    reports a meaningful (if large) error instead of failing.
    """
    if clock.frame_count == 0:
        return 0
    scaled = Fraction(timestamp) * clock.fps
    # Nearest integer with exact half-way values going to the earlier frame:
    # ceil(x - 1/2) rounds ties down and everything else to the nearest frame.
    shifted = scaled - Fraction(1, 2)
    index = -((-shifted.numerator) // shifted.denominator)
    if index < 0:
        index = 0
    elif index >= clock.frame_count:
        index = clock.frame_count - 1
    return index


def align_events(clock: FrameClock, events: Iterable[AudioEvent]) -> AudioSummary:
    """Map every cue to a frame and summarise the signed millisecond error."""
    alignments: list[CueAlignment] = []
    for event in events:
        index = nearest_frame(clock, event.timestamp)
        if clock.frame_count == 0:
            frame_pts = Fraction(0)
        else:
            frame_pts = clock.pts(index)
        error = (event.timestamp - frame_pts) * _MS_PER_SECOND
        alignments.append(
            CueAlignment(
                label=event.label,
                sample=event.sample,
                sample_rate=event.sample_rate,
                cue_seconds=event.timestamp,
                frame_index=index,
                frame_pts=frame_pts,
                error_ms=float(error),
            )
        )
    max_abs = max((abs(a.error_ms) for a in alignments), default=0.0)
    return AudioSummary(alignments=tuple(alignments), max_abs_error_ms=max_abs)


def summarize_events(clock: FrameClock, events: Sequence[AudioEvent]) -> AudioSummary:
    """Convenience wrapper accepting any sequence of events."""
    return align_events(clock, events)


__all__ = [
    "AudioSummary",
    "CueAlignment",
    "align_events",
    "nearest_frame",
    "summarize_events",
]
