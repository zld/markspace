"""Timeline schema parsing and validation.

A timeline is a plain mapping (as loaded from JSON) describing an exact frame
rate, a canvas, an ordered set of layers with independent keyframe tracks,
and integer audio cues. Parsing produces immutable dataclasses; anything
malformed, unsupported, or out of range raises :class:`SchemaError` before
any pixels are touched.

Layer stacking uses an explicit ``order`` integer; ties are broken by the
order of declaration, so the result is always deterministic.
"""

from __future__ import annotations

import posixpath
import re
from dataclasses import dataclass
from fractions import Fraction
from typing import Any, Iterable, Mapping

from .clock import ClockError, FrameClock

MAX_CANVAS_PIXELS = 16_000_000
MAX_FRAMES = 100_000
MAX_CANVAS_DIMENSION = 65_535
BLEND_MODES = frozenset({"normal", "multiply", "screen"})

_RATIONAL_RE = re.compile(r"^\s*[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:/[+-]?\d+)?\s*$")


class SchemaError(ValueError):
    """Raised when a timeline document is malformed or unsupported."""


@dataclass(frozen=True)
class Canvas:
    width: int
    height: int

    @property
    def pixels(self) -> int:
        return self.width * self.height


@dataclass(frozen=True)
class Keyframe:
    """One keyframe: a time in seconds and a value (scalar or x/y pair)."""

    time: Fraction
    value: Fraction | tuple[Fraction, Fraction]


@dataclass(frozen=True)
class Track:
    """A sorted, non-empty keyframe track with clamped linear interpolation."""

    keyframes: tuple[Keyframe, ...]

    def evaluate(self, timestamp: Fraction) -> Fraction | tuple[Fraction, Fraction]:
        """Linearly interpolate at ``timestamp``; clamp outside the range."""
        frames = self.keyframes
        if timestamp <= frames[0].time:
            return frames[0].value
        if timestamp >= frames[-1].time:
            return frames[-1].value
        # Small key counts: a linear scan keeps exact rational arithmetic.
        for previous, current in zip(frames, frames[1:]):
            if timestamp <= current.time:
                span = current.time - previous.time
                if span == 0:
                    return current.value
                ratio = (timestamp - previous.time) / span
                if isinstance(previous.value, tuple):
                    x0, y0 = previous.value
                    x1, y1 = current.value  # type: ignore[misc]
                    return (x0 + (x1 - x0) * ratio, y0 + (y1 - y0) * ratio)
                return previous.value + (current.value - previous.value) * ratio  # type: ignore[operator]
        return frames[-1].value


@dataclass(frozen=True)
class Layer:
    id: str
    source: str
    order: int
    declaration_index: int
    start: Fraction
    end: Fraction
    blend: str
    position: Track
    scale: Track
    opacity: Track

    def is_active(self, timestamp: Fraction) -> bool:
        """Half-open activity: ``start <= t < end``."""
        return self.start <= timestamp < self.end


@dataclass(frozen=True)
class AudioEvent:
    label: str
    sample: int
    sample_rate: int

    @property
    def timestamp(self) -> Fraction:
        return Fraction(self.sample, self.sample_rate)


@dataclass(frozen=True)
class Timeline:
    fps: Fraction
    duration: Fraction
    canvas: Canvas
    layers: tuple[Layer, ...]
    audio_events: tuple[AudioEvent, ...]

    @property
    def clock(self) -> FrameClock:
        return FrameClock.from_rate(self.fps, self.duration)

    @property
    def frame_count(self) -> int:
        return self.clock.frame_count

    def draw_order(self) -> tuple[Layer, ...]:
        """Layers bottom-to-top, stable on declaration order for equal ranks."""
        return tuple(sorted(self.layers, key=lambda layer: (layer.order, layer.declaration_index)))


def parse_timeline(document: Mapping[str, Any]) -> Timeline:
    """Validate ``document`` and return an immutable :class:`Timeline`."""
    raise NotImplementedError("timeline parsing lands in the next patch")


__all__ = [
    "AudioEvent",
    "BLEND_MODES",
    "Canvas",
    "Keyframe",
    "Layer",
    "MAX_CANVAS_PIXELS",
    "MAX_FRAMES",
    "SchemaError",
    "Timeline",
    "Track",
    "parse_timeline",
]
