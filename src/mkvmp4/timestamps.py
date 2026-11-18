"""Timestamp expansion, scaling, and NumPy-based validation."""
from __future__ import annotations

import numpy as np

MAX_SAMPLES_PER_TRACK = 5_000_000
MAX_TIMESCALE = 1_000_000
INT64_SAFE = 1 << 62


class TimestampError(Exception):
    """Timestamp rewrite failed (overflow, empty track, bad rounding)."""


def choose_timescale(track_type: int, sampling_freq: float) -> int:
    """Integer MP4 timescale: audio uses its sample rate, video 90 kHz."""
    if track_type == 2 and sampling_freq:  # TRACK_TYPE_AUDIO
        return min(max(int(round(sampling_freq)), 1000), MAX_TIMESCALE)
    return 90_000


def expand_timestamps(timestamps_units, scale_ns: int, timescale: int,
                      default_duration_ns: int = 0):
    """Rewrite Matroska timestamps into integer MP4 track units.

    ``timestamps_units`` are absolute block timestamps in TimestampScale
    units, in decode order. Returns ``(pts, durations)`` as int64 arrays
    with pts strictly increasing and shifted so the minimum is zero.
    """
    n = len(timestamps_units)
    if n == 0:
        raise TimestampError("track has no samples")
    if n > MAX_SAMPLES_PER_TRACK:
        raise TimestampError(
            f"sample count {n} exceeds limit {MAX_SAMPLES_PER_TRACK}")
    lo, hi = min(timestamps_units), max(timestamps_units)
    if max(abs(lo), abs(hi)) * scale_ns * timescale > INT64_SAFE:
        raise TimestampError("timestamp magnitude would overflow int64")

    ts = np.asarray(timestamps_units, dtype=np.int64)
    num = ts * np.int64(scale_ns) * np.int64(timescale)
    pts = (num + 500_000_000) // 1_000_000_000  # round to nearest tick

    exact = num.astype(np.float64) / 1e9
    if not np.all(np.abs(exact - pts.astype(np.float64)) <= 0.5 + 1e-6):
        raise TimestampError("rounding error exceeds half a tick")

    # Enforce strictly increasing pts (repairs duplicate timestamps by
    # nudging later samples one tick forward). Done before shifting so
    # pts[0] is the track minimum and MP4 dts matches pts exactly.
    pts = np.maximum.accumulate(pts - np.arange(n, dtype=np.int64)) \
        + np.arange(n, dtype=np.int64)
    if not np.all(np.diff(pts) > 0):
        raise TimestampError("could not make timestamps strictly increasing")

    if pts[0] != 0:  # negative or late start: shift the track to zero
        pts = pts - pts[0]

    durations = np.empty(n, dtype=np.int64)
    durations[:-1] = np.diff(pts)
    if default_duration_ns > 0:
        last = (default_duration_ns * timescale + 500_000_000) // 1_000_000_000
    elif n > 1:
        last = int(np.median(durations[:-1]))
    else:
        last = timescale // 25
    durations[-1] = max(int(last), 1)

    if durations.max() >= 1 << 32:
        raise TimestampError("sample duration exceeds 32-bit stts delta")
    if pts[-1] + durations[-1] >= 1 << 63:
        raise TimestampError("track duration overflows int64")
    return pts, durations


def pts_ms(pts_value: int, timescale: int) -> float:
    return pts_value * 1000.0 / timescale
