"""Mix metrics: track stats, automation samples, clipping, path diff."""

from __future__ import annotations

import numpy as np

from .automation import curve_for_track
from .model import Manifest

# Hard limiter bounds applied to the master before writing.
LIMIT = 1.0


def automation_samples(manifest: Manifest, times: list[float]) -> list[dict]:
    """Sample every track's curve at each requested time (dB + linear)."""
    out = []
    for spec in manifest.tracks:
        curve = curve_for_track(spec)
        for t in times:
            db = float(curve.gain_db_at(np.array([t]))[0])
            out.append({
                "track": spec.index,
                "time": t,
                "db": db,
                "gain": float(10.0 ** (db / 20.0)),
            })
    return out


def count_clipped(master: np.ndarray) -> int:
    """Channel samples whose pre-limit absolute value exceeds 1."""
    return int(np.count_nonzero(np.abs(master) > LIMIT))


def limit_master(master: np.ndarray) -> np.ndarray:
    return np.clip(master, -LIMIT, LIMIT)


def max_abs_diff(a: np.ndarray, b: np.ndarray) -> float:
    if a.shape != b.shape:
        return float("inf")
    if a.size == 0:
        return 0.0
    return float(np.abs(a - b).max())


def build_report(
    *,
    rate: int,
    channels: int,
    frames: int,
    track_stats: list[dict],
    automation: list[dict],
    clipped: int,
    diff: float,
) -> dict:
    return {
        "rate": rate,
        "channels": channels,
        "tracks": len(track_stats),
        "frames": frames,
        "track_metrics": track_stats,
        "automation_samples": automation,
        "clipped_samples": clipped,
        "max_abs_diff": diff,
    }
