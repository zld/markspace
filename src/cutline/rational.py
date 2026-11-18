"""Exact rational time arithmetic and frame-rate conversion.

All timeline values are carried as :class:`fractions.Fraction` seconds.  No
binary floating point ever enters the pipeline, so repeated conversions and
round-trip exchanges cannot accumulate drift.

Rounding policy
---------------
Frames are integers, so a conversion from exact seconds to frames has to
round.  We use *round half away from zero*: ``2.5`` frames becomes ``3`` and
``-2.5`` becomes ``-3``.  The choice is applied identically everywhere
(conversion, v1 exchange, OTIO bridge) so the result is reproducible and
symmetric around zero.
"""

from __future__ import annotations

from dataclasses import dataclass
from fractions import Fraction
from typing import Any


class TimeError(ValueError):
    """Invalid time or frame-rate value, with an optional field location."""

    def __init__(self, message: str, path: str | None = None) -> None:
        self.path = path
        super().__init__(f"{path}: {message}" if path else message)


def parse_rate(value: Any, path: str | None = None) -> Fraction:
    """Parse a frame rate into a positive exact rational.

    Accepts integers, ``"24"``, ``"24000/1001"``, ``"29.97"`` and
    :class:`Fraction`.  Floats are rejected outright: they cannot represent
    NTSC rates exactly and invite drift.  Values that only exist as floats
    (for example OpenTimelineIO's ``RationalTime.rate``) go through
    :func:`rate_from_float` instead.
    """
    if isinstance(value, bool):
        raise TimeError("frame rate must be a number or rational string, not bool", path)
    if isinstance(value, Fraction):
        rate = value
    elif isinstance(value, int):
        rate = Fraction(value)
    elif isinstance(value, float):
        raise TimeError("frame rate must not be a float; use '24000/1001' or an integer", path)
    elif isinstance(value, str):
        text = value.strip()
        if not text:
            raise TimeError("frame rate is empty", path)
        try:
            rate = Fraction(text)
        except (ValueError, ZeroDivisionError):
            raise TimeError(f"invalid frame rate {value!r}", path) from None
    else:
        raise TimeError(f"invalid frame rate type {type(value).__name__}", path)

    if rate <= 0:
        raise TimeError(f"frame rate must be positive, got {value!r}", path)
    return rate


def rate_from_float(value: float, path: str | None = None) -> Fraction:
    """Recover an exact rate from a float produced by a third-party library.

    OpenTimelineIO stores rates as C doubles, so ``24000/1001`` arrives as
    ``23.976023976023978``.  We reconstruct the intended rational with a
    bounded denominator, which maps every standard rate (24, 25, 30,
    23.976, 29.97, 59.94, 119.88) back to its exact value without drift.
    """
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise TimeError(f"invalid frame rate {value!r}", path)
    if value <= 0:
        raise TimeError(f"frame rate must be positive, got {value!r}", path)
    return parse_rate(Fraction(str(float(value))).limit_denominator(1_000_000), path)


def round_half_away(value: Fraction) -> int:
    """Round a fraction to the nearest integer, halves away from zero."""
    if value >= 0:
        return int((value + Fraction(1, 2)).__floor__())
    return -int((-value + Fraction(1, 2)).__floor__())


def format_fraction(value: Fraction) -> str:
    """Canonical exact text form: ``"n"`` when integral, else ``"n/d"``."""
    if value.denominator == 1:
        return str(value.numerator)
    return f"{value.numerator}/{value.denominator}"


def parse_seconds(value: Any, path: str | None = None) -> Fraction:
    """Parse an exact seconds value (rational string or integer)."""
    if isinstance(value, bool):
        raise TimeError("seconds must not be bool", path)
    if isinstance(value, Fraction):
        return value
    if isinstance(value, int):
        return Fraction(value)
    if isinstance(value, float):
        raise TimeError("seconds must not be a float; use 'n/d' rational text", path)
    if isinstance(value, str):
        text = value.strip()
        if not text:
            raise TimeError("seconds value is empty", path)
        try:
            return Fraction(text)
        except (ValueError, ZeroDivisionError):
            raise TimeError(f"invalid seconds value {value!r}", path) from None
    raise TimeError(f"invalid seconds type {type(value).__name__}", path)


@dataclass(frozen=True, order=True)
class RationalTime:
    """An exact instant or duration, held as ``Fraction`` seconds."""

    seconds: Fraction

    def __post_init__(self) -> None:
        if not isinstance(self.seconds, Fraction):
            object.__setattr__(self, "seconds", parse_seconds(self.seconds))

    # -- construction -------------------------------------------------
    @classmethod
    def zero(cls) -> "RationalTime":
        return cls(Fraction(0))

    @classmethod
    def from_frames(cls, count: int, rate: Any, path: str | None = None) -> "RationalTime":
        if isinstance(count, bool) or not isinstance(count, int):
            raise TimeError(f"frame count must be an integer, got {count!r}", path)
        return cls(Fraction(count) / parse_rate(rate, path))

    # -- conversion ---------------------------------------------------
    def to_frames(self, rate: Any, path: str | None = None) -> int:
        return round_half_away(self.seconds * parse_rate(rate, path))

    def as_seconds_text(self) -> str:
        return format_fraction(self.seconds)

    # -- arithmetic ---------------------------------------------------
    def __add__(self, other: "RationalTime") -> "RationalTime":
        return RationalTime(self.seconds + other.seconds)

    def __sub__(self, other: "RationalTime") -> "RationalTime":
        return RationalTime(self.seconds - other.seconds)

    def __neg__(self) -> "RationalTime":
        return RationalTime(-self.seconds)

    def max(self, other: "RationalTime") -> "RationalTime":
        return self if self >= other else other

    def min(self, other: "RationalTime") -> "RationalTime":
        return self if self <= other else other

    def is_negative(self) -> bool:
        return self.seconds < 0


