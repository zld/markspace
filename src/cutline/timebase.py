"""Exact rational frame rates, time conversion and half-open frame spans.

The whole toolchain stores rates as :class:`fractions.Fraction` values and
timeline coordinates as integer frame counts at a declared frame rate.  A
seconds value is only accepted when it lands exactly on a frame boundary;
otherwise a :class:`TimebaseError` is raised instead of rounding silently.

Examples
--------
>>> rate = FrameRate(30000, 1001)
>>> rate.frames_to_seconds(30000)
Fraction(1001, 1)
>>> rate.frames_from_seconds(Fraction(1001, 1))
30000
>>> rate.frames_from_seconds(Fraction(1, 100))
Traceback (most recent call last):
    ...
cutline.errors.TimebaseError: ...
"""

from __future__ import annotations

from dataclasses import dataclass
from fractions import Fraction
from typing import Any

from .errors import TimebaseError

Number = int | float | Fraction | str


def _reject_bool(value: Any, field: str) -> None:
    if isinstance(value, bool):
        raise TimebaseError(f"{field} must be a number, not a boolean")


def as_fraction(value: Any, field: str = "value") -> Fraction:
    """Convert a JSON-friendly scalar to an exact fraction.

    Integers and :class:`Fraction` are exact.  Floats must be finite; their
    decimal spelling is used (``0.1`` -> ``1/10``) so that a value written in
    JSON round-trips to the number a human wrote.  Strings accept ``"3/2"``
    and decimal forms.
    """

    _reject_bool(value, field)
    if isinstance(value, Fraction):
        return value
    if isinstance(value, int):
        return Fraction(value)
    if isinstance(value, float):
        if value != value or value in (float("inf"), float("-inf")):
            raise TimebaseError(f"{field} must be finite, got {value!r}")
        return Fraction(str(value))
    if isinstance(value, str):
        text = value.strip()
        if not text:
            raise TimebaseError(f"{field} must not be empty")
        try:
            return Fraction(text)
        except (ValueError, ZeroDivisionError) as exc:
            raise TimebaseError(f"{field} is not a finite rational: {value!r}") from exc
    raise TimebaseError(f"{field} must be a number or rational string, got {type(value).__name__}")


def require_int(value: Any, field: str, *, minimum: int | None = None) -> int:
    """Return an exact integer, rejecting booleans, floats and non-finite values."""

    _reject_bool(value, field)
    if not isinstance(value, int):
        raise TimebaseError(f"{field} must be an integer, got {type(value).__name__}")
    if minimum is not None and value < minimum:
        raise TimebaseError(f"{field} must be >= {minimum}, got {value}")
    return value


def positive_fraction(value: Any, field: str) -> Fraction:
    fraction = as_fraction(value, field)
    if fraction <= 0:
        raise TimebaseError(f"{field} must be > 0, got {fraction}")
    return fraction


@dataclass(frozen=True, slots=True)
class FrameRate:
    """An exact positive frame rate stored in reduced numerator/denominator form."""

    numerator: int
    denominator: int = 1

    def __post_init__(self) -> None:
        _reject_bool(self.numerator, "frame rate numerator")
        _reject_bool(self.denominator, "frame rate denominator")
        if not isinstance(self.numerator, int) or not isinstance(self.denominator, int):
            raise TimebaseError("frame rate numerator and denominator must be integers")
        if self.denominator == 0:
            raise TimebaseError("frame rate denominator must not be zero")
        if self.numerator == 0:
            raise TimebaseError("frame rate must not be zero")
        value = Fraction(self.numerator, self.denominator)
        if value <= 0:
            raise TimebaseError(f"frame rate must be > 0, got {self.numerator}/{self.denominator}")
        object.__setattr__(self, "numerator", value.numerator)
        object.__setattr__(self, "denominator", value.denominator)

    @property
    def value(self) -> Fraction:
        return Fraction(self.numerator, self.denominator)

    @property
    def fps(self) -> float:
        """Approximate value for display only; never used for calculations."""

        return float(self.value)

    def frames_to_seconds(self, frames: Number) -> Fraction:
        count = as_fraction(frames, "frames")
        return count / self.value

    def seconds_to_frames_exact(self, seconds: Number) -> Fraction:
        return as_fraction(seconds, "seconds") * self.value

    def frames_from_seconds(self, seconds: Number) -> int:
        """Map seconds to a frame count, refusing values between frame boundaries."""

        exact = self.seconds_to_frames_exact(seconds)
        if exact.denominator != 1:
            raise TimebaseError(
                f"{exact} frames at rate {self.numerator}/{self.denominator} "
                "does not land on a frame boundary"
            )
        return exact.numerator

    def to_json(self) -> dict[str, int]:
        return {"numerator": self.numerator, "denominator": self.denominator}

    @classmethod
    def from_json(cls, obj: Any, field: str = "frame_rate") -> "FrameRate":
        if isinstance(obj, dict):
            if set(obj) != {"numerator", "denominator"}:
                raise TimebaseError(
                    f"{field} must contain exactly 'numerator' and 'denominator', "
                    f"got {sorted(obj)}"
                )
            numerator = require_int(obj["numerator"], f"{field}.numerator")
            denominator = require_int(obj["denominator"], f"{field}.denominator")
            return cls(numerator, denominator)
        if isinstance(obj, int) and not isinstance(obj, bool):
            return cls(obj, 1)
        raise TimebaseError(f"{field} must be an object with numerator/denominator")

    def __str__(self) -> str:
        if self.denominator == 1:
            return str(self.numerator)
        return f"{self.numerator}/{self.denominator}"


def frames_from_seconds(seconds: Number, rate: FrameRate) -> int:
    return rate.frames_from_seconds(seconds)


def seconds_from_frames(frames: Number, rate: FrameRate) -> Fraction:
    return rate.frames_to_seconds(frames)


@dataclass(frozen=True, slots=True)
class FrameSpan:
    """A half-open span ``[start, start + duration)`` in timeline frames."""

    start: int
    duration: int

    def __post_init__(self) -> None:
        require_int(self.start, "span start", minimum=0)
        require_int(self.duration, "span duration", minimum=0)

    @property
    def end(self) -> int:
        return self.start + self.duration

    @property
    def is_empty(self) -> bool:
        return self.duration == 0

    def overlaps(self, other: "FrameSpan") -> bool:
        return self.start < other.end and other.start < self.end

    def overlap_frames(self, other: "FrameSpan") -> int:
        if not self.overlaps(other):
            return 0
        return min(self.end, other.end) - max(self.start, other.start)

    def intersection(self, other: "FrameSpan") -> "FrameSpan | None":
        if not self.overlaps(other):
            return None
        start = max(self.start, other.start)
        end = min(self.end, other.end)
        return FrameSpan(start, end - start)

    def is_adjacent(self, other: "FrameSpan") -> bool:
        return self.end == other.start or other.end == self.start

    def contains(self, other: "FrameSpan") -> bool:
        return self.start <= other.start and other.end <= self.end

    def touches_or_overlaps(self, other: "FrameSpan") -> bool:
        return self.start <= other.end and other.start <= self.end
