"""Signal statistics and terminal report assembly."""
from __future__ import annotations

import numpy as np

from .curve import gain_db_at


def signal_stats(samples):
    """Peak (max absolute value) and RMS of a signal block."""
    samples = np.asarray(samples, dtype=np.float64)
    if samples.size == 0:
        return {"peak": 0.0, "rms": 0.0}
    peak = float(np.max(np.abs(samples)))
    rms = float(np.sqrt(np.mean(np.square(samples))))
    return {"peak": peak, "rms": rms}


def mix_stats(mix):
    """Mix statistics; clipping_count counts samples with |x| > 1.0.

    The float output itself is never clipped — over-range values are
    kept and only counted here.
    """
    stats = signal_stats(mix)
    stats["clipping_count"] = int(np.count_nonzero(np.abs(mix) > 1.0))
    return stats


def automation_report(named_curves, times):
    """Sample each track's gain curve at the given times (seconds)."""
    report = []
    for name, curve in named_curves:
        for t in times:
            report.append({
                "track": name,
                "time": round(float(t), 6),
                "db": round(gain_db_at(curve, float(t)), 6),
            })
    return report


def build_report(sample_rate, mix, track_reports, automation, max_abs_error):
    """Assemble the JSON-serialisable terminal report."""
    stats = mix_stats(mix)
    return {
        "sample_rate": int(sample_rate),
        "frames": int(mix.shape[0]),
        "channels": int(mix.shape[1]) if mix.ndim == 2 else 0,
        "tracks": track_reports,
        "automation_samples": automation,
        "peak": stats["peak"],
        "rms": stats["rms"],
        "clipping_count": stats["clipping_count"],
        "offline_stream_max_abs_error": max_abs_error,
    }
