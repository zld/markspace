"""Loudness and clipping metrics.

All levels are computed on *unclipped* floating-point data so that an
overloaded bus is never hidden by the numbers reported on it.  Digital
silence yields ``-inf`` dBFS; the report layer maps that to JSON ``null``.
"""

from __future__ import annotations

import math

import numpy as np


def peak_dbfs(samples: np.ndarray) -> float:
    """Peak absolute amplitude in dBFS over every channel/sample."""
    if samples.size == 0:
        return -math.inf
    peak = float(np.max(np.abs(samples)))
    if peak <= 0.0:
        return -math.inf
    return 20.0 * math.log10(peak)


def rms_dbfs(samples: np.ndarray) -> float:
    """Root-mean-square level in dBFS over every channel/sample."""
    if samples.size == 0:
        return -math.inf
    rms = float(np.sqrt(np.mean(np.square(samples, dtype=np.float64))))
    if rms <= 0.0:
        return -math.inf
    return 20.0 * math.log10(rms)


def clipping_stats(samples: np.ndarray) -> dict[str, float | int]:
    """Count full-scale exceedances and report the raw peak.

    A sample clips when ``|sample| > 1.0`` on any channel.  The peak is
    reported from the same unclipped data so overloads remain locatable.
    """
    if samples.size == 0:
        return {"sample_count": 0, "peak": 0.0}
    magnitude = np.abs(samples)
    count = int(np.count_nonzero(magnitude > 1.0))
    return {"sample_count": count, "peak": float(np.max(magnitude))}


def json_safe(value: float) -> float | None:
    """Map non-finite floats (e.g. silence ``-inf``) to JSON ``null``."""
    if isinstance(value, (int, np.integer)):
        return int(value)
    if math.isfinite(float(value)):
        return float(value)
    return None
