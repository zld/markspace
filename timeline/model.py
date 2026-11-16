"""Timeline, layer, clip and keyframe data model.

Clips use left-closed, right-open frame intervals ``[start_frame, end_frame)``.
Layers are composited in list order (first layer is at the back).
"""
from __future__ import annotations

from dataclasses import dataclass, field

import numpy as np

BLEND_MODES: tuple[str, ...] = ("source-over", "multiply", "screen")

DEFAULT_MAX_DIMENSION = 4096
DEFAULT_MAX_FRAMES = 72000  # 10 minutes at 120 fps


class TimelineError(ValueError):
    """Raised when timeline structure or parameters are invalid."""


@dataclass(frozen=True)
class Keyframe:
    """Animated state at a given frame: opacity and canvas offset."""

    frame: int
    opacity: float
    x: float
    y: float


@dataclass
class Clip:
    """A source image placed on the canvas over a frame interval."""

    name: str
    start_frame: int
    end_frame: int
    blend_mode: str
    keyframes: tuple[Keyframe, ...]
    image: np.ndarray  # RGBA uint8, shape (h, w, 4)

    def __post_init__(self) -> None:
        if self.end_frame < self.start_frame:
            raise TimelineError(
                f"clip {self.name!r}: end_frame {self.end_frame} < "
                f"start_frame {self.start_frame}"
            )
        if self.blend_mode not in BLEND_MODES:
            raise TimelineError(
                f"clip {self.name!r}: invalid blend mode {self.blend_mode!r}; "
                f"expected one of {BLEND_MODES}"
            )
        if self.image.ndim != 3 or self.image.shape[2] != 4:
            raise TimelineError(
                f"clip {self.name!r}: image must have shape (h, w, 4), "
                f"got {self.image.shape}"
            )
        if self.image.dtype != np.uint8:
            raise TimelineError(
                f"clip {self.name!r}: image dtype must be uint8, "
                f"got {self.image.dtype}"
            )
        frames = [kf.frame for kf in self.keyframes]
        if frames != sorted(frames):
            raise TimelineError(
                f"clip {self.name!r}: keyframes must be sorted by frame"
            )

    @property
    def is_empty(self) -> bool:
        """An empty clip covers no frames and is skipped by the compositor."""
        return self.end_frame <= self.start_frame

    def active(self, frame_index: int) -> bool:
        """Left-closed, right-open interval membership."""
        return self.start_frame <= frame_index < self.end_frame

    def sample(self, frame_index: int) -> tuple[float, float, float]:
        """Interpolate ``(opacity, x, y)`` at ``frame_index``."""
        if not self.keyframes:
            return 1.0, 0.0, 0.0
        kfs = self.keyframes
        if frame_index <= kfs[0].frame:
            kf = kfs[0]
            return kf.opacity, kf.x, kf.y
        if frame_index >= kfs[-1].frame:
            kf = kfs[-1]
            return kf.opacity, kf.x, kf.y
        for prev, nxt in zip(kfs, kfs[1:]):
            if prev.frame <= frame_index <= nxt.frame:
                span = nxt.frame - prev.frame
                if span <= 0:
                    return nxt.opacity, nxt.x, nxt.y
                t = (frame_index - prev.frame) / span
                return (
                    prev.opacity + (nxt.opacity - prev.opacity) * t,
                    prev.x + (nxt.x - prev.x) * t,
                    prev.y + (nxt.y - prev.y) * t,
                )
        kf = kfs[-1]  # unreachable, kept for safety
        return kf.opacity, kf.x, kf.y


@dataclass
class Layer:
    """An ordered group of clips composited as one z-level."""

    name: str
    clips: list[Clip] = field(default_factory=list)


@dataclass
class Timeline:
    """A fixed-size, fixed-length sequence of composited frames."""

    fps: int
    frame_count: int
    width: int
    height: int
    layers: list[Layer] = field(default_factory=list)

    def validate(
        self,
        max_dimension: int = DEFAULT_MAX_DIMENSION,
        max_frames: int = DEFAULT_MAX_FRAMES,
    ) -> None:
        """Enforce structural limits; raises TimelineError on violation."""
        if self.frame_count < 1:
            raise TimelineError(
                f"timeline has zero frames (frame_count={self.frame_count})"
            )
        if self.frame_count > max_frames:
            raise TimelineError(
                f"frame_count {self.frame_count} exceeds limit {max_frames}"
            )
        for label, dim in (("width", self.width), ("height", self.height)):
            if dim < 1 or dim > max_dimension:
                raise TimelineError(
                    f"{label} {dim} out of range [1, {max_dimension}]"
                )
