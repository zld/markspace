"""Deterministic timeline overlap resolution pipeline.

All time math is performed on an integer frame scale using exact rational
arithmetic (:class:`fractions.Fraction`); floating point is never used to
accumulate time.
"""

from .model import (
    Clip,
    FrameInterval,
    ResolutionError,
    ResolvedInterval,
    Timeline,
    Track,
    frames_from_seconds,
)

__all__ = [
    "Clip",
    "FrameInterval",
    "ResolutionError",
    "ResolvedInterval",
    "Timeline",
    "Track",
    "frames_from_seconds",
]
