"""Deterministic rational-arithmetic frame clock.

The clock maps a timeline duration into a fixed number of frames and each
frame index into an exact rational presentation timestamp. Fractions are
kept as :class:`fractions.Fraction` so that rates such as 30000/1001 never
accumulate floating-point drift.

Frame coverage follows a half-open interval convention: frame ``i`` covers
``[t_i, t_{i+1})``. The number of frames is therefore the ceiling of
``duration / frame_period`` -- a duration that is not aligned to a frame
boundary still yields a deterministic, non-zero count.
"""

from __future__ import annotations

from dataclasses import dataclass
from fractions import Fraction


class ClockError(ValueError):
    """Raised when frame timing parameters are invalid or out of range."""


@dataclass(frozen=True)
class FrameClock:
    """Exact rational frame clock.

    Attributes:
        fps: Frame rate as an exact positive :class:`Fraction`.
        duration_seconds: Timeline duration as an exact non-negative Fraction.
        frame_count: Number of frames whose half-open coverage starts before
            the end of the timeline.
    """

    fps: Fraction
    duration_seconds: Fraction
    frame_count: int

    @property
    def frame_period(self) -> Fraction:
        """Exact duration of one frame in seconds."""
        return Fraction(1, 1) / self.fps

    @classmethod
    def from_rate(
        cls,
        fps: Fraction | int | str,
        duration_seconds: Fraction | int | str,
    ) -> "FrameClock":
        """Build a clock from a rational rate and duration.

        ``fps`` may be an int, a Fraction, or a string such as ``"30000/1001"``.
        """
        rate = _as_positive_fraction(fps, "fps")
        duration = _as_non_negative_fraction(duration_seconds, "duration_seconds")
        period = Fraction(1, 1) / rate
        # Ceiling division keeps a partially covered trailing frame.
        count = _ceil_div(duration, period)
        return cls(fps=rate, duration_seconds=duration, frame_count=count)

    def pts(self, index: int) -> Fraction:
        """Return the exact start timestamp of ``index`` in seconds."""
        self._check_index(index)
        return Fraction(index, 1) / self.fps

    def frame_index_at(self, timestamp: Fraction) -> int:
        """Return the frame whose half-open interval contains ``timestamp``."""
        if timestamp < 0:
            return 0
        return int(timestamp * self.fps)

    def frame_interval(self, index: int) -> tuple[Fraction, Fraction]:
        """Return the half-open ``[start, end)`` interval of ``index``."""
        self._check_index(index)
        return self.pts(index), self.pts(index + 1)

    def _check_index(self, index: int) -> None:
        if not isinstance(index, int) or isinstance(index, bool):
            raise ClockError(f"frame index must be an int, got {type(index).__name__}")
        if index < 0 or index >= self.frame_count:
            raise ClockError(
                f"frame index {index} outside [0, {self.frame_count})"
            )


def _ceil_div(numerator: Fraction, denominator: Fraction) -> int:
    """Ceiling of ``numerator / denominator`` for non-negative fractions."""
    if denominator <= 0:
        raise ClockError("frame period must be positive")
    quotient = numerator / denominator
    return -((-quotient.numerator) // quotient.denominator)


def _as_positive_fraction(value: Fraction | int | str, name: str) -> Fraction:
    fraction = _as_fraction(value, name)
    if fraction <= 0:
        raise ClockError(f"{name} must be positive, got {value!r}")
    return fraction


def _as_non_negative_fraction(value: Fraction | int | str, name: str) -> Fraction:
    fraction = _as_fraction(value, name)
    if fraction < 0:
        raise ClockError(f"{name} must be non-negative, got {value!r}")
    return fraction


def _as_fraction(value: Fraction | int | str, name: str) -> Fraction:
    if isinstance(value, bool):
        raise ClockError(f"{name} must be a rational number, got bool")
    if isinstance(value, Fraction):
        return value
    if isinstance(value, int):
        return Fraction(value, 1)
    if isinstance(value, str):
        text = value.strip()
        try:
            return Fraction(text)
        except (ValueError, ZeroDivisionError) as exc:
            raise ClockError(f"{name} is not a valid rational: {value!r}") from exc
    raise ClockError(f"{name} must be int, Fraction, or string, got {type(value).__name__}")
