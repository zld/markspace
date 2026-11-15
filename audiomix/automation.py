"""Gain automation curves sampled at absolute project frames.

A :class:`GainCurve` is built from validated :class:`~audiomix.model.GainPoint`
objects.  The mode stored on a point governs the segment *starting* at that
point and ending at the next one:

* ``hold``   - the point's gain is held constant until the next point;
* ``linear`` - the gain ramps linearly to the next point's gain.

Before the first point the first gain is held; after the last point the last
gain is held ("endpoints keep the head/tail gain").  With no automation
points the curve is flat at ``1.0``.  The track's static ``gain`` acts as a
trim multiplier on top of the curve.
"""

from __future__ import annotations

import numpy as np

from .model import HOLD, GainPoint


class GainCurve:
    def __init__(self, base_gain: float, points: tuple[GainPoint, ...] = ()):
        self.base_gain = float(base_gain)
        self.points = tuple(points)
        if self.points:
            self._frames = np.array([p.frame for p in self.points], dtype=np.int64)
            self._gains = np.array([p.gain for p in self.points], dtype=np.float64)
            self._modes = [p.mode for p in self.points]
        else:
            self._frames = np.empty(0, dtype=np.int64)
            self._gains = np.empty(0, dtype=np.float64)
            self._modes = []

    def sample(self, frame: int) -> float:
        """Curve value (without base trim) at one absolute frame."""
        return float(self.sample_frames(frame, 1)[0])

    def sample_frames(self, start: int, count: int) -> np.ndarray:
        """Return ``count`` curve values for frames ``start .. start+count-1``."""
        if count <= 0:
            return np.empty(0, dtype=np.float64)
        frames = np.arange(start, start + count, dtype=np.int64)
        if not self.points:
            values = np.ones(count, dtype=np.float64)
            return values * self.base_gain
        idx = np.searchsorted(self._frames, frames, side="right") - 1
        values = np.empty(count, dtype=np.float64)
        last = len(self.points) - 1
        before = idx < 0
        values[before] = self._gains[0]
        tail = idx >= last
        values[tail] = self._gains[last]
        interior = ~(before | tail)
        if np.any(interior):
            ii = idx[interior]
            f0 = self._frames[ii]
            f1 = self._frames[ii + 1]
            g0 = self._gains[ii]
            g1 = self._gains[ii + 1]
            x = frames[interior]
            held = np.array([m == HOLD for m in self._modes], dtype=bool)
            seg_hold = held[ii]
            t = (x - f0).astype(np.float64) / (f1 - f0).astype(np.float64)
            ramp = g0 + t * (g1 - g0)
            values[interior] = np.where(seg_hold, g0, ramp)
        return values * self.base_gain
