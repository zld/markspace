"""Gain automation curves positioned on the output-frame timeline.

Three interpolation modes are supported between control points:

* ``hold``   -- step: keep the previous point's value.
* ``linear`` -- straight line in dB.
* ``cubic``  -- monotone cubic (Fritsch-Carlson PCHIP) in dB; never
  overshoots, so a monotonic point sequence stays monotonic.

Outside the point range the endpoint values are held.  Evaluation is a
pure function of the absolute output frame, so offline and chunked
streaming renders sample the identical curve.
"""

from __future__ import annotations

import numpy as np

from .errors import ManifestError
from .manifest import AutomationPoint


def db_to_linear(db: np.ndarray | float) -> np.ndarray | float:
    return 10.0 ** (np.asarray(db) / 20.0)


def _edge_slope(h0: float, h1: float, d0: float, d1: float) -> float:
    """One-sided endpoint derivative for PCHIP (shape-preserving)."""
    m = ((2.0 * h0 + h1) * d0 - h0 * d1) / (h0 + h1)
    if np.sign(m) != np.sign(d0):
        m = 0.0
    elif np.sign(d0) != np.sign(d1) and abs(m) > 3.0 * abs(d0):
        m = 3.0 * d0
    return m


def _pchip_slopes(x: np.ndarray, y: np.ndarray) -> np.ndarray:
    n = len(x)
    if n == 1:
        return np.zeros(1)
    h = np.diff(x)
    d = np.diff(y) / h
    if n == 2:
        return np.array([d[0], d[0]])
    m = np.zeros(n)
    for k in range(1, n - 1):
        if d[k - 1] == 0.0 or d[k] == 0.0 or np.sign(d[k - 1]) != np.sign(d[k]):
            m[k] = 0.0
        else:
            w1 = 2.0 * h[k] + h[k - 1]
            w2 = h[k] + 2.0 * h[k - 1]
            m[k] = (w1 + w2) / (w1 / d[k - 1] + w2 / d[k])
    m[0] = _edge_slope(h[0], h[1], d[0], d[1])
    m[-1] = _edge_slope(h[-1], h[-2], d[-1], d[-2])
    return m


class GainCurve:
    """A validated automation curve in dB over output frames."""

    def __init__(self, points: tuple[AutomationPoint, ...] | list[AutomationPoint], mode: str = "linear"):
        if mode not in ("hold", "linear", "cubic"):
            raise ManifestError(f"unknown interpolation mode {mode!r}")
        if not points:
            raise ManifestError("automation curve needs at least one point")
        frames = [p.frame for p in points]
        if any(b <= a for a, b in zip(frames, frames[1:])):
            raise ManifestError("automation frames must be strictly increasing")
        if not all(np.isfinite(p.db) for p in points):
            raise ManifestError("automation gains must be finite")
        self.mode = mode
        self.x = np.asarray(frames, dtype=np.float64)
        self.y = np.asarray([p.db for p in points], dtype=np.float64)
        self._slopes = _pchip_slopes(self.x, self.y) if mode == "cubic" else None

    def gain_db_at(self, frames: np.ndarray) -> np.ndarray:
        """Evaluate the curve in dB at absolute output frames."""
        f = np.asarray(frames, dtype=np.float64)
        x, y = self.x, self.y
        idx = np.clip(np.searchsorted(x, f, side="right") - 1, 0, len(x) - 1)
        if self.mode == "hold" or len(x) == 1:
            return y[idx]
        seg = np.clip(idx, 0, len(x) - 2)
        above = f >= x[-1]
        seg = np.where(above, len(x) - 2, seg)
        h = x[seg + 1] - x[seg]
        t = np.clip((f - x[seg]) / h, 0.0, 1.0)
        if self.mode == "linear":
            return y[seg] + t * (y[seg + 1] - y[seg])
        # cubic: Hermite basis with PCHIP slopes
        m = self._slopes
        t2, t3 = t * t, t * t * t
        h00 = 2.0 * t3 - 3.0 * t2 + 1.0
        h10 = t3 - 2.0 * t2 + t
        h01 = -2.0 * t3 + 3.0 * t2
        h11 = t3 - t2
        return h00 * y[seg] + h10 * h * m[seg] + h01 * y[seg + 1] + h11 * h * m[seg + 1]

    def linear_at(self, frames: np.ndarray) -> np.ndarray:
        """Evaluate as a linear amplitude multiplier."""
        return np.asarray(db_to_linear(self.gain_db_at(frames)), dtype=np.float64)

    def sample_points(self) -> list[dict]:
        """Control points as report rows: frame, db and linear gain."""
        return [
            {"frame": int(x), "db": float(y), "linear": float(db_to_linear(y))}
            for x, y in zip(self.x, self.y)
        ]
