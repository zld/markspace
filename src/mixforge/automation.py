"""Piecewise gain automation curves and mute segments.

A curve is a pure function of the project frame index, so evaluating it at
block boundaries yields exactly the same values as evaluating the full
timeline at once — this is what keeps offline and stream renders identical.
"""

from __future__ import annotations

import math

import numpy as np

from .project import TrackSpec

NEG_INF_DB = -1200.0  # JSON-safe stand-in for negative infinity on muted points


class GainCurve:
    """Track gain (dB) plus automation keyframes and mute intervals."""

    def __init__(self, spec: TrackSpec, project_rate: int) -> None:
        self.rate = project_rate
        self.base_db = spec.gain_db
        self.times = np.array([p.time for p in spec.automation], dtype=np.float64)
        self.gains = np.array([p.gain_db for p in spec.automation], dtype=np.float64)
        self.hold = np.array([p.interpolation == "hold" for p in spec.automation],
                             dtype=bool)
        self.mutes = [(m.start, m.end) for m in spec.mutes]

    def automation_db(self, times: np.ndarray) -> np.ndarray:
        """Automation keyframe values (dB, before track gain) at ``times``."""
        times = np.asarray(times, dtype=np.float64)
        if self.times.size == 0:
            return np.zeros_like(times)
        # right-side insertion: index of the first point strictly after t.
        idx = np.searchsorted(self.times, times, side="right")
        result = np.empty_like(times)
        # Before the first keyframe, automation contributes no gain (0 dB).
        before = idx == 0
        result[before] = 0.0
        # At or after the last keyframe, hold its value.
        at_end = idx >= len(self.times)
        result[at_end] = self.gains[-1]
        middle = ~(before | at_end)
        if middle.any():
            i = idx[middle]
            t0, t1 = self.times[i - 1], self.times[i]
            g0, g1 = self.gains[i - 1], self.gains[i]
            fraction = (times[middle] - t0) / (t1 - t0)
            interp = np.where(self.hold[i], 0.0, fraction)
            result[middle] = g0 + (g1 - g0) * interp
        return result

    def total_db(self, times: np.ndarray) -> np.ndarray:
        """Total gain in dB (track gain + automation), mutes excluded."""
        return self.base_db + self.automation_db(times)

    def mute_mask(self, times: np.ndarray) -> np.ndarray:
        times = np.asarray(times, dtype=np.float64)
        mask = np.zeros(times.shape, dtype=bool)
        for start, end in self.mutes:
            mask |= (times >= start) & (times < end)
        return mask

    def multiplier(self, frame_start: int, count: int) -> np.ndarray:
        """Linear gain multiplier for frames ``[frame_start, frame_start+count)``."""
        frames = np.arange(frame_start, frame_start + count, dtype=np.float64)
        times = frames / self.rate
        db = self.total_db(times)
        gain = np.power(10.0, db / 20.0)
        gain[self.mute_mask(times)] = 0.0
        return gain

    def summary(self, interval: float, project_frames: int) -> list[dict]:
        """Curve values sampled every ``interval`` seconds across the project."""
        duration = project_frames / self.rate if self.rate else 0.0
        count = int(math.floor(duration / interval)) + 1
        sample_times = np.arange(count, dtype=np.float64) * interval
        mask = self.mute_mask(sample_times)
        db = self.total_db(sample_times)
        out: list[dict] = []
        for t, value, muted in zip(sample_times, db, mask, strict=True):
            out.append({
                "time": round(float(t), 9),
                "gain_db": NEG_INF_DB if muted else round(float(value), 6),
                "muted": bool(muted),
            })
        return out
