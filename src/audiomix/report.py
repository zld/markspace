"""Render statistics and JSON reporting.

Statistics are accumulated block by block so memory stays O(channels):

* ``peak``            - max absolute sample across channels
* ``rms``             - root mean square over valid (non-silent) frames
* ``active_frames``   - frames containing at least one non-zero sample
* ``clipped_samples`` - individual samples with ``abs(x) > 1`` before any
                        limiter is applied

Tracks with no valid frames report zeroed values plus ``"no_active": true``.
"""
from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import numpy as np

from .model import RenderError


class StatsAccumulator:
    """Streaming statistics for one track or the mix bus (float64).

    All reductions are independent of block boundaries: square sums use
    pairwise (Neumaier) accumulation while active-frame and clipping counts
    are integer counts over the same global sample set.
    """

    def __init__(self, name: str):
        self.name = name
        self._compensated_sum = 0.0
        self._compensation = 0.0
        self._peak = 0.0
        self._active_frames = 0
        self._clipped = 0
        self._total_frames = 0
        self._channels = 0

    def add(self, block: np.ndarray) -> None:
        if block.size == 0:
            return
        if not np.isfinite(block).all():
            raise RenderError(
                f"non-finite sample reached statistics for {self.name!r}",
                track=self.name)
        self._channels = block.shape[1]
        # Neumaier compensated summation over block-level partial sums.
        partial = float(np.sum(block * block, dtype=np.float64))
        total = self._compensated_sum + partial
        if abs(self._compensated_sum) >= abs(partial):
            self._compensation += (
                self._compensated_sum - total) + partial
        else:
            self._compensation += (partial - total) + self._compensated_sum
        self._compensated_sum = total
        self._peak = max(self._peak, float(np.max(np.abs(block))))
        self._active_frames += int(np.count_nonzero(
            np.any(block != 0.0, axis=1)))
        self._clipped += int(np.count_nonzero(np.abs(block) > 1.0))
        self._total_frames += int(block.shape[0])

    def as_dict(self) -> dict[str, Any]:
        sample_count = self._active_frames * max(self._channels, 1)
        total_sum = self._compensated_sum + self._compensation
        rms = (np.sqrt(total_sum / sample_count)
               if sample_count > 0 else 0.0)
        out = {
            "peak": float(self._peak),
            "rms": float(rms),
            "active_frames": int(self._active_frames),
            "clipped_samples": int(self._clipped),
            "total_frames": int(self._total_frames),
        }
        if self._active_frames == 0:
            out["no_active"] = True
        return out


def automation_samples(timeline, times: list[float]) -> list[dict[str, Any]]:
    """Report the actual linear gain applied at each requested time."""
    from .automation import AutomationCurve

    samples: list[dict[str, Any]] = []
    for track in timeline.tracks:
        if track.automation is None:
            continue
        curve = AutomationCurve(track.automation)
        for t in times:
            gain = float(curve.sample(float(t)))
            samples.append({
                "track": track.name,
                "time_sec": float(t),
                "gain": gain,
            })
    return samples


def build_report(timeline, track_stats: dict[str, StatsAccumulator],
                 bus_stats: StatsAccumulator, output_frames: int,
                 sample_times: list[float] | None = None,
                 limited: bool = False) -> dict[str, Any]:
    report: dict[str, Any] = {
        "output_rate": timeline.output_rate,
        "channels": timeline.channels,
        "output_frames": int(output_frames),
        "track_count": len(timeline.tracks),
        "limiter_applied": bool(limited),
        "tracks": {name: acc.as_dict()
                   for name, acc in track_stats.items()},
        "bus": bus_stats.as_dict(),
        "automation_samples": automation_samples(
            timeline, sample_times or []),
    }
    return report


def write_report(report: dict[str, Any], path: str | Path) -> None:
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + ".part")
    try:
        tmp.write_text(json.dumps(report, indent=2, sort_keys=True),
                       encoding="utf-8")
        tmp.replace(path)
    except OSError as exc:
        tmp.unlink(missing_ok=True)
        raise RenderError(f"cannot write report {path}: {exc}") from exc
