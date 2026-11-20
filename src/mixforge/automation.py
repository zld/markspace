"""Gain automation: piecewise-linear decibel curves.

Keyframes are interpolated linearly *in the dB domain*; before the first
and after the last keyframe the endpoint values are held. Evaluation is
per-sample in continuous time, so non-integer frame positions and
keyframes landing exactly on block boundaries need no special casing.
"""

from __future__ import annotations

import numpy as np

from .model import AutomationError, Keyframe


class GainCurve:
    """A sorted dB keyframe curve with endpoint hold."""

    def __init__(self, keyframes: tuple[Keyframe, ...] | list[Keyframe]):
        ordered = sorted(keyframes, key=lambda k: k.time)
        for prev, cur in zip(ordered, ordered[1:]):
            if cur.time == prev.time:
                raise AutomationError(f"duplicate keyframe time {cur.time}")
        if not ordered:
            ordered = [Keyframe(time=0.0, db=0.0)]
        self._times = np.array([k.time for k in ordered], dtype=np.float64)
        self._dbs = np.array([k.db for k in ordered], dtype=np.float64)

    @property
    def times(self) -> np.ndarray:
        return self._times

    @property
    def dbs(self) -> np.ndarray:
        return self._dbs

    def db_at(self, times: np.ndarray | float) -> np.ndarray:
        """Interpolated dB gain at the given time(s), endpoints held."""
        return np.interp(times, self._times, self._dbs)

    def linear_at(self, times: np.ndarray | float) -> np.ndarray:
        """Linear amplitude gain at the given time(s)."""
        return np.power(10.0, self.db_at(times) / 20.0)

    def gains_for_frames(
        self, start_frame: int, count: int, sample_rate: int, track_offset: float = 0.0
    ) -> np.ndarray:
        """Per-sample linear gains for ``count`` frames from ``start_frame``.

        ``track_offset`` is the track start in seconds on the timeline;
        keyframe times are track-local, so timeline times are shifted by it.
        """
        frames = start_frame + np.arange(count, dtype=np.float64)
        times = frames / float(sample_rate) - track_offset
        return self.linear_at(times)
