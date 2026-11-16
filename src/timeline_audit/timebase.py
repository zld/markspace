"""Rational frame-rate and sample-rate timebase handling.

All timeline math uses fractions.Fraction so rates like 30000/1001 are
exact. Frame intervals are half-open: frame n covers [n/fps, (n+1)/fps).
"""

from __future__ import annotations

from fractions import Fraction


def parse_fps(value) -> Fraction:
    """Parse a frame rate from str/int/float/Fraction into a positive Fraction.

    Accepts "30000/1001", "29.97", "30", 30, 29.97. Raises ValueError on
    malformed input or non-positive rates.
    """
    if isinstance(value, Fraction):
        fps = value
    elif isinstance(value, bool):
        raise ValueError(f"invalid fps {value!r}")
    elif isinstance(value, int):
        fps = Fraction(value)
    elif isinstance(value, float):
        fps = Fraction(value).limit_denominator(1_000_000)
    elif isinstance(value, str):
        text = value.strip()
        if "/" in text:
            num, _, den = text.partition("/")
            try:
                fps = Fraction(int(num.strip()), int(den.strip()))
            except (ValueError, ZeroDivisionError) as exc:
                raise ValueError(f"invalid fps {value!r}: {exc}") from exc
        else:
            try:
                fps = Fraction(text)
            except ValueError as exc:
                raise ValueError(f"invalid fps {value!r}: {exc}") from exc
    else:
        raise ValueError(f"invalid fps {value!r}")
    if fps <= 0:
        raise ValueError(f"fps must be positive, got {fps}")
    return fps


def format_fps(fps: Fraction) -> str:
    """Canonical display form: '30000/1001' or '30'."""
    if fps.denominator == 1:
        return str(fps.numerator)
    return f"{fps.numerator}/{fps.denominator}"


def frame_interval(fps: Fraction, index: int) -> tuple[Fraction, Fraction]:
    """Half-open interval [start, end) in seconds for frame ``index``."""
    start = Fraction(index) / fps
    return start, start + 1 / fps


def frame_count(fps: Fraction, duration: Fraction) -> int:
    """Number of frames in ``duration`` seconds, rounding a partial tail up.

    A duration of zero yields zero frames; a duration shorter than one
    frame still yields one frame.
    """
    if duration < 0:
        raise ValueError(f"duration must be >= 0, got {duration}")
    total = duration * fps
    n = total.numerator // total.denominator
    if total.numerator % total.denominator:
        n += 1
    return n


def nearest_frame(fps: Fraction, t: Fraction) -> int:
    """Frame index nearest to time ``t`` seconds (ties round up)."""
    scaled = t * fps
    return (2 * scaled.numerator + scaled.denominator) // (2 * scaled.denominator)


def sample_to_frame(fps: Fraction, sample_rate: int, sample_index: int) -> int:
    """Map an audio sample index to the nearest video frame index."""
    return nearest_frame(fps, Fraction(sample_index, sample_rate))


def to_seconds(value) -> Fraction:
    """Convert an int/float/str number of seconds to an exact Fraction."""
    if isinstance(value, Fraction):
        return value
    if isinstance(value, bool):
        raise ValueError(f"invalid seconds {value!r}")
    if isinstance(value, int):
        return Fraction(value)
    if isinstance(value, float):
        return Fraction(value).limit_denominator(1_000_000)
    if isinstance(value, str):
        try:
            return Fraction(value.strip())
        except ValueError as exc:
            raise ValueError(f"invalid seconds {value!r}") from exc
    raise ValueError(f"invalid seconds {value!r}")
