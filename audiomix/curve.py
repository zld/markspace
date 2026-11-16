"""Gain automation curves defined by (seconds, dB) points.

Curves interpolate linearly in time (in the dB domain) and hold the
endpoint values outside the defined range.  Invalid definitions —
duplicate time points, non-finite values, out-of-range gains — are
rejected at validation time.
"""
from __future__ import annotations

import math

import numpy as np

# Gains beyond +-120 dB are treated as authoring mistakes, not automation.
MAX_ABS_DB = 120.0


class CurveError(ValueError):
    """Raised when a gain curve definition is invalid."""


def validate_points(points, track_name="<curve>"):
    """Validate raw ``(time_seconds, db)`` pairs and return a clean list.

    Points are sorted by time; duplicate time points, non-finite values
    and gains outside +-MAX_ABS_DB are rejected with CurveError.
    """
    if points is None:
        raise CurveError(f"track '{track_name}': gain curve is missing")
    pts = []
    for raw in points:
        try:
            t, db = float(raw[0]), float(raw[1])
        except (TypeError, IndexError, ValueError) as exc:
            raise CurveError(
                f"track '{track_name}': malformed curve point {raw!r}"
            ) from exc
        if not math.isfinite(t) or t < 0.0:
            raise CurveError(f"track '{track_name}': invalid curve time {t!r}")
        if not math.isfinite(db) or abs(db) > MAX_ABS_DB:
            raise CurveError(f"track '{track_name}': invalid gain {db!r} dB")
        pts.append((t, db))
    if not pts:
        raise CurveError(f"track '{track_name}': gain curve needs at least one point")
    pts.sort(key=lambda p: p[0])
    for (t0, _), (t1, _) in zip(pts, pts[1:]):
        if t1 == t0:
            raise CurveError(f"track '{track_name}': duplicate curve time {t0}")
    return pts


def _tables(points):
    times = np.array([p[0] for p in points], dtype=np.float64)
    dbs = np.array([p[1] for p in points], dtype=np.float64)
    return times, dbs


def gain_db_at(points, t):
    """Curve value in dB at time ``t`` (seconds), clamped to endpoints."""
    times, dbs = _tables(points)
    return float(np.interp(float(t), times, dbs))


def gain_linear_at(points, frame_indices, sample_rate):
    """Linear gain factors for absolute output frame indices.

    ``np.interp`` already holds endpoint values outside the curve range,
    which is exactly the required clamping behaviour.
    """
    times, dbs = _tables(points)
    t = np.asarray(frame_indices, dtype=np.float64) / float(sample_rate)
    db = np.interp(t, times, dbs)
    return np.power(10.0, db / 20.0)
