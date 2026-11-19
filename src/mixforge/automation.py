"""Piecewise gain curves generated from automation nodes.

Time is kept in seconds internally and converted to sample positions by the
caller, so offline and blockwise streaming share the exact same time base.
Outside the first/last node the curve holds the endpoint value.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np

from .model import AutomationCurve, INTERPOLATIONS


@dataclass(frozen=True)
class CurveSample:
    frame: int
    time: float
    gain: float


def evaluate(curve: AutomationCurve, times: np.ndarray) -> np.ndarray:
    """Evaluate the curve at arbitrary time points (seconds)."""

    times = np.asarray(times, dtype=np.float64)
    node_times = np.array([node.time for node in curve.nodes], dtype=np.float64)
    node_gains = np.array([node.gain for node in curve.nodes], dtype=np.float64)
    if curve.interpolation == "hold":
        # Right-continuous steps: a node's value applies until the next node.
        indices = np.searchsorted(node_times, times, side="right") - 1
        indices = np.clip(indices, 0, len(node_gains) - 1)
        return node_gains[indices]
    if curve.interpolation != "linear":  # pragma: no cover - guarded by model
        raise ValueError(f"unsupported interpolation: {curve.interpolation}")
    return np.interp(times, node_times, node_gains,
                     left=node_gains[0], right=node_gains[-1])


def gain_for_frames(curve: AutomationCurve, start_frame: int, frame_count: int,
                    sample_rate: int) -> np.ndarray:
    """Gain values for the half-open sample range [start_frame, start+n)."""

    if frame_count <= 0:
        return np.zeros(0, dtype=np.float64)
    frames = np.arange(start_frame, start_frame + frame_count, dtype=np.float64)
    times = frames / sample_rate
    return evaluate(curve, times)


def sample_curve(curve: AutomationCurve, total_frames: int, sample_rate: int,
                 fixed_points: int = 5) -> list[CurveSample]:
    """Deterministic summary samples: each node plus a fixed time grid.

    Node times are snapped to their nearest sample, then a uniform grid of
    ``fixed_points`` positions is added. The result is deduplicated by frame
    and sorted, giving at least one sample per node and never fewer than
    ``fixed_points`` samples for a non-empty timeline.
    """

    collected: dict[int, CurveSample] = {}
    for node in curve.nodes:
        frame = int(round(node.time * sample_rate))
        frame = min(max(frame, 0), max(total_frames - 1, 0))
        collected[frame] = CurveSample(
            frame=frame,
            time=frame / sample_rate,
            gain=float(evaluate(curve, np.array([frame / sample_rate]))[0]),
        )
    if total_frames > 0:
        grid = np.linspace(0, total_frames - 1, fixed_points).round().astype(int)
        for frame in grid:
            frame = int(frame)
            if frame not in collected:
                time_value = frame / sample_rate
                collected[frame] = CurveSample(
                    frame=frame,
                    time=time_value,
                    gain=float(evaluate(curve, np.array([time_value]))[0]),
                )
    return [collected[key] for key in sorted(collected)]


def supported_interpolations() -> tuple[str, ...]:
    return INTERPOLATIONS
