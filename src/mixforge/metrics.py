"""Peak, RMS and clipping statistics for rendered audio.

Accumulation is streaming-safe: peak is a max, sum-of-squares is additive,
so per-block and whole-session statistics agree bit-for-bit.

Clipping is defined as absolute amplitude >= 1.0 in the float signal,
counted per sample.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np

CLIP_THRESHOLD = 1.0


def _as_2d(samples: np.ndarray) -> np.ndarray:
    data = np.asarray(samples, dtype=np.float64)
    if data.ndim == 1:
        data = data[:, None]
    if data.ndim != 2:
        raise ValueError("metrics expect a 1-D or 2-D array")
    return data


@dataclass
class StatsAccumulator:
    peak: float = 0.0
    sumsq: float = 0.0
    samples: int = 0
    frames: int = 0
    clip_count: int = 0
    channels: int = 0

    def add(self, samples: np.ndarray) -> None:
        data = _as_2d(samples)
        if data.size == 0:
            return
        channels = data.shape[1]
        if self.channels not in (0, channels):
            raise ValueError("cannot mix channel counts in one accumulator")
        self.channels = channels
        self.peak = max(self.peak, float(np.max(np.abs(data))))
        self.sumsq += float(np.sum(data * data))
        self.samples += int(data.size)
        self.frames += int(data.shape[0])
        self.clip_count += int(np.count_nonzero(np.abs(data) >= CLIP_THRESHOLD))

    @property
    def rms(self) -> float:
        if self.samples == 0:
            return 0.0
        return float(np.sqrt(self.sumsq / self.samples))

    def as_dict(self) -> dict[str, float | int]:
        return {
            "peak": round(self.peak, 8),
            "rms": round(self.rms, 8),
            "clip_count": self.clip_count,
            "frames": self.frames,
            "channels": self.channels,
        }


def measure(samples: np.ndarray) -> dict[str, float | int]:
    acc = StatsAccumulator()
    acc.add(samples)
    return acc.as_dict()


def clip_positions(samples: np.ndarray, limit: int = 8) -> list[dict[str, int]]:
    """First clipped samples as frame/channel positions, for localisation."""
    data = _as_2d(samples)
    frames, channels = np.nonzero(np.abs(data) >= CLIP_THRESHOLD)
    return [{"frame": int(f), "channel": int(c)}
            for f, c in zip(frames[:limit], channels[:limit])]
