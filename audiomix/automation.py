"""Gain automation curves.

Curves are defined by (time_seconds, db) points, interpolated linearly in
the dB domain and held constant outside the endpoint range.  Structural
validation (duplicate times, ordering, finiteness) happens in
:mod:`audiomix.model`; this module evaluates the curve and converts to
linear gain.
"""

from __future__ import annotations

import numpy as np

from .model import TrackSpec


def db_to_linear(db: np.ndarray) -> np.ndarray:
    return np.power(10.0, db / 20.0)


class GainCurve:
    """Piecewise-linear (in dB) gain envelope for one track."""

    def __init__(self, points):
        # points: tuple[CurvePoint, ...]; empty means constant unity gain.
        self._times = np.array([p.time for p in points], dtype=np.float64)
        self._db = np.array([p.db for p in points], dtype=np.float64)

    @property
    def is_constant(self) -> bool:
        return self._times.size == 0

    def gain_db_at(self, times: np.ndarray) -> np.ndarray:
        """Interpolated dB value at each time; clamps to endpoints outside."""
        times = np.asarray(times, dtype=np.float64)
        if self._times.size == 0:
            return np.zeros_like(times)
        return np.interp(times, self._times, self._db)

    def linear_gain_at(self, times: np.ndarray) -> np.ndarray:
        return db_to_linear(self.gain_db_at(times))

    def apply(self, samples: np.ndarray, start_frame: int, rate: int) -> np.ndarray:
        """Return ``samples`` (frames, channels) scaled by the envelope.

        ``start_frame`` is the absolute timeline frame index of row 0, so
        block processing and whole-file processing produce identical gains.
        """
        n = samples.shape[0]
        if self._times.size == 0:
            return samples
        t = (start_frame + np.arange(n, dtype=np.float64)) / float(rate)
        gain = self.linear_gain_at(t)[:, None]
        return samples * gain


def curve_for_track(track: TrackSpec) -> GainCurve:
    return GainCurve(track.curve)
