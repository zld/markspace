"""Piecewise-linear gain automation on the integer frame clock."""

from __future__ import annotations

import math
from dataclasses import dataclass, field

import numpy as np

from .model import TARGET_SAMPLE_RATE


@dataclass
class AutomationCurve:
    """Segment points sorted by frame index (strictly increasing).

    The curve is defined over frames ``[0, frames_total)``. Before the first
    point and after the last point the endpoint value is held. A curve with no
    points is a constant 0.0 dB.
    """

    frames: np.ndarray = field(default_factory=lambda: np.empty(0, dtype=np.int64))
    gain_db: np.ndarray = field(default_factory=lambda: np.empty(0, dtype=np.float64))
    frames_total: int = 0

    def __post_init__(self) -> None:
        self.frames = np.asarray(self.frames, dtype=np.int64)
        self.gain_db = np.asarray(self.gain_db, dtype=np.float64)
        if self.frames.ndim != 1 or self.gain_db.ndim != 1 or self.frames.shape != self.gain_db.shape:
            raise ValueError("automation frames and gain_db must be 1-D and equal length")
        if np.any(np.diff(self.frames) <= 0):
            raise ValueError("automation frames must be strictly increasing")
        if self.frames.size and self.frames_total > 0 and self.frames[-1] > self.frames_total:
            raise ValueError("automation point lies beyond the project length")
        if not np.all(np.isfinite(self.gain_db)):
            raise ValueError("automation gain_db must be finite")


def build_curve(points: tuple[tuple[float, float], ...] | list[tuple[float, float]],
                frames_total: int,
                sample_rate: int = TARGET_SAMPLE_RATE) -> AutomationCurve:
    """Convert ``(seconds, dB)`` points to integer frames using half-up rounding."""
    frames: list[int] = []
    gains: list[float] = []
    for seconds, db in points:
        if not math.isfinite(seconds) or not math.isfinite(db):
            raise ValueError("automation points must be finite")
        if seconds < 0:
            raise ValueError("automation time must be >= 0")
        frame = int(seconds * sample_rate + 0.5)
        if frame > frames_total:
            raise ValueError("automation point lies beyond the project length")
        if frames and frame <= frames[-1]:
            raise ValueError("automation frames must be strictly increasing after rounding")
        frames.append(frame)
        gains.append(db)
    return AutomationCurve(
        frames=np.asarray(frames, dtype=np.int64),
        gain_db=np.asarray(gains, dtype=np.float64),
        frames_total=frames_total,
    )


def gain_at(curve: AutomationCurve, frames: np.ndarray) -> np.ndarray:
    """Return interpolated dB values for an array of frame indices.

    Vectorised and deterministic; the same function feeds the offline and
    streaming paths so both observe identical curves.
    """
    frames = np.asarray(frames, dtype=np.int64)
    if curve.frames.size == 0:
        return np.zeros(frames.shape, dtype=np.float64)
    px = curve.frames
    py = curve.gain_db
    if px.size == 1:
        return np.full(frames.shape, py[0], dtype=np.float64)
    idx = np.searchsorted(px, frames, side="right") - 1
    idx = np.clip(idx, 0, px.size - 2)
    x0 = px[idx]
    x1 = px[idx + 1]
    y0 = py[idx]
    y1 = py[idx + 1]
    t = (frames - x0) / (x1 - x0)
    values = y0 + (y1 - y0) * t
    values = np.where(frames < px[0], py[0], values)
    values = np.where(frames >= px[-1], py[-1], values)
    return values


def db_to_linear(db: np.ndarray | float) -> np.ndarray:
    return np.power(10.0, np.asarray(db, dtype=np.float64) / 20.0)


def automation_samples(curve: AutomationCurve, count: int) -> list[tuple[int, float]]:
    """Sample ``count`` frames across the project length for reporting."""
    if count <= 0 or curve.frames_total <= 0:
        return []
    if count == 1:
        positions = np.array([0], dtype=np.int64)
    else:
        raw = np.linspace(0, curve.frames_total - 1, count)
        positions = np.floor(raw + 0.5).astype(np.int64)
        positions = np.unique(positions)
    values = gain_at(curve, positions)
    return [(int(f), float(g)) for f, g in zip(positions, values)]


def automation_samples_at(curve: AutomationCurve,
                          frames: list[int]) -> list[tuple[int, float]]:
    """Evaluate the curve at caller-specified frames, preserving their order."""
    if not frames:
        return []
    positions = np.asarray(frames, dtype=np.int64)
    values = gain_at(curve, positions)
    return [(int(f), float(g)) for f, g in zip(positions, values)]
