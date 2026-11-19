"""Peak, RMS, frame and clip statistics with explicit signal domains."""

from __future__ import annotations

import math
from dataclasses import dataclass, field
from typing import Any

import numpy as np

from .errors import ProcessingError

#: Clip counting domain.  ``pre_limit`` is recorded on the master summary.
DOMAIN_TRACK = "track_post_gain"
DOMAIN_BUS = "bus_post_gain"
DOMAIN_MASTER = "master_pre_limit"


@dataclass
class SignalStats:
    samples: int = 0
    frames: int = 0
    channels: int = 0
    peak: float = 0.0
    sum_squares: float = 0.0
    clip_count: int = 0
    non_finite_count: int = 0

    @property
    def rms(self) -> float:
        if self.samples == 0:
            return 0.0
        return math.sqrt(self.sum_squares / self.samples)

    def feed(self, data: np.ndarray, *, location: str) -> None:
        array = np.asarray(data, dtype=np.float64)
        if array.ndim != 2:
            raise ProcessingError("statistics need a 2-D array", location=location)
        if not np.all(np.isfinite(array)):
            raise ProcessingError("non-finite samples reached statistics", location=location)
        if array.size == 0:
            return
        magnitude = np.abs(array)
        self.frames += int(array.shape[0])
        self.channels = max(self.channels, int(array.shape[1]))
        self.samples += int(array.size)
        self.peak = max(self.peak, float(magnitude.max()))
        self.sum_squares += float(np.dot(array.ravel(), array.ravel()))
        self.clip_count += int(np.count_nonzero(magnitude >= 1.0))

    def as_dict(self, *, domain: str) -> dict[str, Any]:
        return {
            "frames": self.frames,
            "samples": self.samples,
            "channels": self.channels,
            "peak": _clean(self.peak),
            "rms": _clean(self.rms),
            "clip_count": int(self.clip_count),
            "clip_domain": domain,
        }


def _clean(value: float) -> float:
    """Round summary numbers for compact, deterministic JSON output."""

    if not math.isfinite(value):
        raise ProcessingError("non-finite statistic", location="stats")
    return float(f"{value:.15g}")


def compare_samples(
    offline: np.ndarray, streaming: np.ndarray
) -> tuple[float, int | None]:
    """Return ``(max_abs_diff, first_diff_sample)`` over flattened samples.

    ``first_diff_sample`` is a flat sample index in row-major (frame-major)
    order, or ``None`` when the streams are identical (or both empty).
    """

    left = np.asarray(offline)
    right = np.asarray(streaming)
    if left.shape != right.shape:
        raise ProcessingError(
            "offline and streaming outputs have different shapes",
            location="compare",
            context={"offline_shape": list(left.shape), "stream_shape": list(right.shape)},
        )
    if left.size == 0:
        return 0.0, None
    diff = np.abs(left.astype(np.float64) - right.astype(np.float64))
    max_diff = float(diff.max())
    if not math.isfinite(max_diff):
        raise ProcessingError("comparison produced non-finite differences", location="compare")
    if max_diff == 0.0:
        return 0.0, None
    first = int(np.argmax(diff.ravel() != 0))
    return max_diff, first


@dataclass
class TrackReport:
    id: str
    stats: SignalStats
    source_sample_rate: int
    source_frames: int
    clip_frames: int
    start_frame: int
    mute: bool
    bus: str | None
    automation_frames: int = 0

    def as_dict(self) -> dict[str, Any]:
        data = {"id": self.id, **self.stats.as_dict(domain=DOMAIN_TRACK)}
        data.update(
            {
                "source_sample_rate": self.source_sample_rate,
                "source_frames": self.source_frames,
                "clip_frames": self.clip_frames,
                "start_frame": self.start_frame,
                "mute": self.mute,
                "bus": self.bus,
                "automation_keyframes": self.automation_frames,
            }
        )
        return data


@dataclass
class Summary:
    sample_rate: int
    channels: int
    frames: int
    block_size: int
    mode: str
    tracks: list[TrackReport] = field(default_factory=list)
    buses: dict[str, SignalStats] = field(default_factory=dict)
    master: SignalStats = field(default_factory=SignalStats)
    automation_samples: list[dict[str, Any]] = field(default_factory=list)
    extra: dict[str, Any] = field(default_factory=dict)

    def as_dict(self) -> dict[str, Any]:
        payload: dict[str, Any] = {
            "status": "ok",
            "mode": self.mode,
            "sample_rate": self.sample_rate,
            "channels": self.channels,
            "frames": self.frames,
            "block_size": self.block_size,
            "track_count": len(self.tracks),
            "bus_count": len(self.buses),
            "tracks": [track.as_dict() for track in self.tracks],
            "buses": {
                bus_id: stats.as_dict(domain=DOMAIN_BUS)
                for bus_id, stats in self.buses.items()
            },
            "master": self.master.as_dict(domain=DOMAIN_MASTER),
            "automation_samples": self.automation_samples,
        }
        payload.update(self.extra)
        return payload
