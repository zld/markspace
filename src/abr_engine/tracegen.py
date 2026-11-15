"""Deterministic synthetic network-trace generation (NumPy, seeded)."""
from __future__ import annotations

import numpy as np


def generate_trace(seed: int, segments: int, segment_seconds: float = 4.0) -> dict:
    """Build a reproducible piecewise-constant bandwidth trace.

    The trace mixes a lognormal random walk with burst congestion, brief
    zero-bandwidth outages, missing samples, and varying sample intervals.
    Duration covers twice the ideal playback time so stalls never run off
    the end of the trace.
    """
    if segments < 1:
        raise ValueError("segments must be >= 1")
    if segment_seconds <= 0:
        raise ValueError("segment_seconds must be > 0")
    rng = np.random.default_rng(seed)
    duration = segments * segment_seconds * 2.0

    samples: list[dict] = []
    t = 0.0
    bw = float(rng.uniform(1.5e6, 4.0e6))
    congestion_left = 0
    congestion_factor = 1.0
    zero_left = 0
    while t < duration:
        if zero_left > 0:
            current: float | None = 0.0
            zero_left -= 1
        elif congestion_left > 0:
            current = bw * congestion_factor
            congestion_left -= 1
        else:
            bw = float(np.clip(bw * np.exp(rng.normal(0.0, 0.15)), 1e5, 8e6))
            current = bw
            r = rng.random()
            if r < 0.04:  # sudden congestion burst
                congestion_left = int(rng.integers(2, 8))
                congestion_factor = float(rng.uniform(0.05, 0.3))
            elif r < 0.05:  # brief zero-bandwidth outage
                zero_left = int(rng.integers(1, 3))
        if rng.random() < 0.02:  # missing observation
            current = None
        samples.append({"time": round(t, 6), "bandwidth": current})
        t += float(rng.uniform(0.5, 2.0))

    if not samples[-1]["bandwidth"]:
        samples[-1]["bandwidth"] = bw  # keep the tail downloadable
    return {
        "seed": seed,
        "segments": segments,
        "segment_seconds": segment_seconds,
        "samples": samples,
    }
