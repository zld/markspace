"""Automation curves: parsing, evaluation and deterministic sampling.

A curve is a list of ``(ms, value, unit)`` points. Evaluation is piecewise
linear in the value domain (linear gain or dB, converted to linear gain at
each node), and holds the endpoint value outside the curve.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np

from .errors import ManifestError
from .fields import finite_number

UNITS = ("linear", "db")


@dataclass(frozen=True)
class AutomationPoint:
    ms: float
    value: float
    unit: str


@dataclass(frozen=True)
class AutomationCurve:
    points: tuple[AutomationPoint, ...]

    def __post_init__(self) -> None:
        if len(self.points) < 2:
            raise ValueError("automation curve needs at least 2 points")

    def gain_at_ms(self, ms: float) -> float:
        """Gain at ``ms``; endpoint hold outside the curve range."""
        return float(self.gains_at_ms(np.array([ms], dtype=np.float64))[0])

    def gains_at_ms(self, ms: np.ndarray) -> np.ndarray:
        """Vectorised evaluation at the given millisecond times."""
        times = np.asarray(ms, dtype=np.float64)
        knots_ms = np.array([p.ms for p in self.points], dtype=np.float64)
        knots_gain = np.array([_point_gain(p) for p in self.points],
                              dtype=np.float64)
        return np.interp(times, knots_ms, knots_gain)

    def sampled(self, times_ms: np.ndarray) -> "SampledCurve":
        return SampledCurve(times_ms=np.asarray(times_ms, dtype=np.float64),
                            gains=self.gains_at_ms(times_ms))


@dataclass(frozen=True)
class SampledCurve:
    """A curve evaluated at known times; serialisable for review."""

    times_ms: np.ndarray
    gains: np.ndarray

    def as_list(self) -> list[dict[str, float]]:
        return [
            {"ms": float(ms), "gain": float(gain)}
            for ms, gain in zip(self.times_ms, self.gains)
        ]


def _point_gain(point: AutomationPoint) -> float:
    if point.unit == "db":
        return 10.0 ** (point.value / 20.0)
    return point.value


def parse_automation(raw, where: str, *, track_id: str | None = None) -> AutomationCurve | None:
    """Validate a raw automation array and build a curve."""
    if raw is None:
        return None
    if not isinstance(raw, list):
        raise ManifestError("automation must be an array of points",
                            field=where, track_id=track_id)
    if len(raw) < 2:
        raise ManifestError("automation needs at least 2 points",
                            field=where, track_id=track_id)
    points: list[AutomationPoint] = []
    for index, item in enumerate(raw):
        item_where = f"{where}[{index}]"
        if not isinstance(item, dict):
            raise ManifestError("automation point must be an object",
                                field=item_where, track_id=track_id)
        unknown = set(item) - {"ms", "value", "unit"}
        if unknown:
            raise ManifestError(f"unknown field(s): {', '.join(sorted(unknown))}",
                                field=item_where, track_id=track_id)
        for required in ("ms", "value"):
            if required not in item:
                raise ManifestError(f"automation point is missing '{required}'",
                                    field=f"{item_where}.{required}",
                                    track_id=track_id)
        ms = finite_number(item["ms"], f"{item_where}.ms", track_id=track_id)
        if ms < 0:
            raise ManifestError("automation ms must be >= 0",
                                field=f"{item_where}.ms", track_id=track_id)
        value = finite_number(item["value"], f"{item_where}.value",
                              track_id=track_id)
        unit = item.get("unit", "linear")
        if unit not in UNITS:
            raise ManifestError("unit must be 'linear' or 'db'",
                                field=f"{item_where}.unit", track_id=track_id)
        if points:
            if abs(ms - points[-1].ms) <= 1e-9:
                raise ManifestError("duplicate automation time point",
                                    field=f"{item_where}.ms", track_id=track_id)
            if ms < points[-1].ms:
                raise ManifestError("automation points must be in increasing order",
                                    field=f"{item_where}.ms", track_id=track_id)
        points.append(AutomationPoint(ms=ms, value=value, unit=unit))
    return AutomationCurve(tuple(points))
