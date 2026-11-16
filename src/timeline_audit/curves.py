"""Keyframe evaluation for animatable layer properties.

Supports linear and hold (step) interpolation. The ``interp`` tag on a
keyframe selects how values evolve from that keyframe to the next one.
Outside the keyed range the nearest endpoint value is held.
"""

from __future__ import annotations

from dataclasses import dataclass

INTERP_MODES = ("linear", "hold")


@dataclass(frozen=True)
class Keyframe:
    frame: int
    value: float
    interp: str = "linear"

    def __post_init__(self):
        if self.interp not in INTERP_MODES:
            raise ValueError(f"invalid interp {self.interp!r}")


def evaluate(keys: tuple[Keyframe, ...], frame: int) -> float:
    """Evaluate a sorted, non-empty keyframe track at ``frame``."""
    if not keys:
        raise ValueError("cannot evaluate an empty keyframe track")
    if frame <= keys[0].frame:
        return keys[0].value
    if frame >= keys[-1].frame:
        return keys[-1].value
    lo, hi = 0, len(keys) - 1
    while lo + 1 < hi:
        mid = (lo + hi) // 2
        if keys[mid].frame <= frame:
            lo = mid
        else:
            hi = mid
    a, b = keys[lo], keys[hi]
    if a.interp == "hold":
        return a.value
    span = b.frame - a.frame
    t = (frame - a.frame) / span
    return a.value + (b.value - a.value) * t
