"""Automation gain curves sampled on the output frame grid.

Rules (explicit and tested):

* ``linear`` mode interpolates linear gains directly.
* ``db`` mode interpolates in decibel space and converts back with
  ``10 ** (db / 20)``.  A ``null`` point value means negative infinity dB,
  i.e. linear gain zero.  A segment touching -inf dB yields zero gain in its
  interior; the finite endpoint keeps its value exactly at the breakpoint.
* Zero linear gain is a plain zero (no dB conversion is attempted).
* Times outside the breakpoint range extend the nearest endpoint value.
"""
from __future__ import annotations

import numpy as np

from .model import AutomationSpec

_NEG_INF = np.float64(-np.inf)


class AutomationCurve:
    """Immutable sampled representation of one track's automation."""

    def __init__(self, spec: AutomationSpec):
        self.mode = spec.mode
        self.times = np.array([p.time for p in spec.points],
                              dtype=np.float64)
        gains = np.array([p.as_linear_gain() for p in spec.points],
                         dtype=np.float64)
        self.gains = gains
        if self.mode == "db":
            db = np.full(len(spec.points), _NEG_INF, dtype=np.float64)
            finite = np.array([p.value is not None for p in spec.points])
            db[finite] = [p.value for p in spec.points if p.value is not None]
            self.db = db
        else:
            self.db = None

    def sample(self, t: np.ndarray | float) -> np.ndarray | float:
        """Return linear gain at time(s) *t* (seconds)."""
        scalar = np.isscalar(t)
        times = np.atleast_1d(np.asarray(t, dtype=np.float64))
        n = self.times.size
        if n == 1:
            out = np.full(times.shape, self.gains[0], dtype=np.float64)
            return float(out[0]) if scalar else out

        # Endpoint extension: outside the breakpoint range hold the end value.
        out = np.empty(times.shape, dtype=np.float64)
        out[times <= self.times[0]] = self.gains[0]
        out[times >= self.times[-1]] = self.gains[-1]
        interior = (times > self.times[0]) & (times < self.times[-1])
        if not interior.any():
            return float(out[0]) if scalar else out

        ti = times[interior]
        idx = np.searchsorted(self.times, ti, side="right") - 1
        idx = np.clip(idx, 0, n - 2)
        t0 = self.times[idx]
        t1 = self.times[idx + 1]
        g0 = self.gains[idx]
        g1 = self.gains[idx + 1]
        frac = (ti - t0) / (t1 - t0)
        at_left = ti == t0
        at_right = ti == t1

        if self.mode == "linear":
            seg = g0 + frac * (g1 - g0)
        else:
            db0 = self.db[idx]
            db1 = self.db[idx + 1]
            both_finite = np.isfinite(db0) & np.isfinite(db1)
            seg = np.zeros(ti.shape, dtype=np.float64)
            if both_finite.any():
                db = db0[both_finite] + frac[both_finite] * (
                    db1[both_finite] - db0[both_finite])
                seg[both_finite] = 10.0 ** (db / 20.0)
            # Mixed -inf segment: interior is zero; only breakpoints retain
            # their exact finite gain value.
            mixed = ~both_finite
            if mixed.any():
                take_left = mixed & at_left
                take_right = mixed & at_right
                seg[take_left] = g0[take_left]
                seg[take_right] = g1[take_right]
        # Breakpoints must equal their declared point values in every mode.
        seg[at_left] = g0[at_left]
        seg[at_right] = g1[at_right]
        out[interior] = seg
        return float(out[0]) if scalar else out

    def frame_gains(self, frame_indices: np.ndarray, rate: int) -> np.ndarray:
        """Sample the curve at absolute output frame indices."""
        times = frame_indices.astype(np.float64) / float(rate)
        return self.sample(times)
