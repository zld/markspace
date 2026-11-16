"""Rational frame clock.

Frame boundaries are computed with exact rational arithmetic so that
frame ``i`` always covers the left-closed, right-open interval
``[i/fps, (i+1)/fps)`` regardless of fps.
"""
from __future__ import annotations

from dataclasses import dataclass
from fractions import Fraction

MIN_FPS = 1
MAX_FPS = 120


class ClockError(ValueError):
    """Raised when clock parameters are out of the supported range."""


@dataclass(frozen=True)
class FrameClock:
    """Maps frame indices to rational presentation-time intervals."""

    fps: int

    def __post_init__(self) -> None:
        if isinstance(self.fps, bool) or not isinstance(self.fps, int):
            raise ClockError(f"fps must be an integer, got {self.fps!r}")
        if not MIN_FPS <= self.fps <= MAX_FPS:
            raise ClockError(
                f"fps {self.fps} out of supported range [{MIN_FPS}, {MAX_FPS}]"
            )

    @property
    def frame_duration(self) -> Fraction:
        """Exact duration of one frame in seconds."""
        return Fraction(1, self.fps)

    def frame_start(self, index: int) -> Fraction:
        """Exact presentation time (seconds) at which ``index`` becomes visible."""
        if index < 0:
            raise ClockError(f"frame index {index} must be >= 0")
        return Fraction(index, self.fps)

    def frame_end(self, index: int) -> Fraction:
        """Exact time at which ``index`` stops being visible (exclusive)."""
        return self.frame_start(index + 1)

    def frame_interval(self, index: int) -> tuple[Fraction, Fraction]:
        """Left-closed, right-open interval ``[start, end)`` for a frame."""
        return self.frame_start(index), self.frame_end(index)

    def frames_for_duration(self, seconds: float) -> int:
        """Frame count for a duration in seconds, rounded to nearest frame."""
        if isinstance(seconds, bool) or not isinstance(seconds, (int, float)):
            raise ClockError(f"duration must be a number, got {seconds!r}")
        exact = Fraction(str(seconds)) * self.fps
        # Round half to even on exact rationals; never negative.
        return max(0, int(exact + Fraction(1, 2)))
