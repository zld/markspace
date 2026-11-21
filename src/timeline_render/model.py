"""Timeline data model.

All time values are integers (frames or samples) or exact Fractions so that
compositing and audio alignment are reproducible across platforms.
"""

from __future__ import annotations

from dataclasses import dataclass
from fractions import Fraction
from pathlib import Path

BLEND_MODES = ("normal", "add", "multiply")

# Resource limits (constraint: bounded frames, layers, input pixels).
MAX_FRAMES = 100_000
MAX_LAYERS = 64
MAX_INPUT_PIXELS = 64_000_000  # width * height * layer count


class InputError(Exception):
    """Invalid user input. `path` is a JSON-style field path."""

    def __init__(self, path: str, message: str) -> None:
        self.path = path
        super().__init__(f"{path}: {message}")


class ComposeError(Exception):
    """Failure while compositing a frame."""


@dataclass(frozen=True)
class FrameRate:
    """Rational frame rate: num/den frames per second, both positive."""

    num: int
    den: int


@dataclass(frozen=True)
class Keyframe:
    frame: int
    value: Fraction  # opacity in [0, 1]


@dataclass(frozen=True)
class Layer:
    """One PNG layer visible on the half-open range [start_frame, end_frame)."""

    name: str
    png: str
    start_frame: int
    end_frame: int
    opacity: Fraction  # static opacity; Fraction(1) when keyframed at rest
    keyframes: tuple[Keyframe, ...]  # empty -> use static opacity
    blend: str
    z: int
    order: int  # input order; stabilizes sort for equal z


@dataclass(frozen=True)
class AudioEvent:
    name: str
    sample: int  # position in samples at the timeline sample rate


@dataclass(frozen=True)
class Timeline:
    frame_rate: FrameRate
    frame_count: int
    layers: tuple[Layer, ...]
    sample_rate: int  # 0 -> no audio events
    events: tuple[AudioEvent, ...]
    base_dir: Path  # layer PNG paths resolve relative to this directory

    def frame_start_sample(self, frame: int) -> Fraction:
        """Exact sample position of the start boundary of `frame`."""
        if self.sample_rate <= 0:
            raise ValueError("timeline has no audio sample rate")
        return Fraction(
            frame * self.frame_rate.den * self.sample_rate, self.frame_rate.num
        )
