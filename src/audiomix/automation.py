"""Automation curves: parsing, validation, and deterministic gain sampling.

Curve semantics
---------------
* Points live at *project-sample* frame indices (absolute session frames, not
  seconds), so a curve is independent of track placement and block size.
* A curve uses a single unit, either ``"linear"`` (amplitude) or ``"db"``.
  Linear curves interpolate amplitude; dB curves interpolate in the dB domain
  and convert with ``10 ** (db / 20)``. Mixing units in one curve is rejected
  because it has no unambiguous meaning.
* Extrapolation is clamped: frames before the first point hold the first
  point's gain, frames after the last point hold the last point's gain.
* Duplicate timestamps, non-finite values, negative linear gains, negative or
  non-integer frames, and empty curves are rejected with a clear reason.
"""

from __future__ import annotations

import math
from dataclasses import dataclass
from typing import Any, Iterable, Mapping, Sequence

import numpy as np

from .errors import AutomationError

UNITS = ("linear", "db")
_UNIT_LABELS = {"linear": "linear", "db": "dB"}


@dataclass(frozen=True)
class AutomationPoint:
    """One gain keyframe at a project frame."""

    frame: int
    value: float
    unit: str


@dataclass(frozen=True)
class AutomationCurve:
    """A validated, ordered gain curve with clamped extrapolation."""

    points: tuple[AutomationPoint, ...]

    @property
    def unit(self) -> str:
        return self.points[0].unit

    def gain_at(self, frames: np.ndarray | int | float) -> np.ndarray:
        """Return the gain at the given project frame(s).

        Uses ``numpy.interp`` on the curve's native unit domain and clamps
        outside the curve, matching the documented extrapolation rule.
        """
        frames_arr = np.asarray(frames, dtype=np.float64)
        frames_arr = np.where(frames_arr < 0.0, 0.0, frames_arr)
        key_frames = np.array([p.frame for p in self.points], dtype=np.float64)
        key_values = np.array([p.value for p in self.points], dtype=np.float64)
        sampled = np.interp(frames_arr, key_frames, key_values)
        if self.unit == "db":
            return np.power(10.0, sampled / 20.0)
        return sampled

    def sampled_points(self, start: int, end: int) -> list[dict[str, Any]]:
        """Report the start, every contained keyframe, and the end.

        ``start`` and ``end`` are inclusive project frames.
        """
        if end < start:
            end = start
        frames = {int(start), int(end)}
        frames.update(p.frame for p in self.points if start <= p.frame <= end)
        return [
            {
                "frame": frame,
                "value": float(self.gain_at(frame)),
                "unit": "gain",
                "source_unit": self.unit,
            }
            for frame in sorted(frames)
        ]


def default_curve() -> AutomationCurve:
    """Unity gain with a single point at frame zero."""
    return AutomationCurve((AutomationPoint(0, 1.0, "linear"),))


def _is_int(value: Any) -> bool:
    return isinstance(value, int) and not isinstance(value, bool)


def _parse_point(raw: Mapping[str, Any], track: str, index: int) -> AutomationPoint:
    if not isinstance(raw, Mapping):
        raise AutomationError(f"track '{track}': automation point {index} must be an object")
    if "frame" not in raw or "value" not in raw:
        raise AutomationError(
            f"track '{track}': automation point {index} needs 'frame' and 'value'"
        )
    frame = raw["frame"]
    value = raw["value"]
    unit = raw.get("unit", "linear")
    if unit not in UNITS:
        raise AutomationError(
            f"track '{track}': automation point {index} has unknown unit {unit!r}; "
            f"expected one of {', '.join(UNITS)}"
        )
    if not _is_int(frame) or frame < 0:
        raise AutomationError(
            f"track '{track}': automation point {index} frame must be a non-negative "
            f"integer, got {frame!r}"
        )
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise AutomationError(
            f"track '{track}': automation point {index} value must be a number, got {value!r}"
        )
    numeric = float(value)
    if not math.isfinite(numeric):
        raise AutomationError(
            f"track '{track}': automation point {index} value must be finite, got {value!r}"
        )
    if unit == "linear" and numeric < 0.0:
        raise AutomationError(
            f"track '{track}': automation point {index} has negative linear gain {numeric}"
        )
    return AutomationPoint(int(frame), numeric, unit)


def validate_points(points: Iterable[AutomationPoint], track: str) -> AutomationCurve:
    """Sort and validate already-parsed points, rejecting duplicate frames."""
    ordered = sorted(points, key=lambda p: p.frame)
    if not ordered:
        raise AutomationError(f"track '{track}': automation curve has no points")
    units = {p.unit for p in ordered}
    if len(units) > 1:
        raise AutomationError(
            f"track '{track}': automation curve mixes units "
            f"({', '.join(sorted(units))}); use one unit per curve"
        )
    for previous, current in zip(ordered, ordered[1:]):
        if current.frame == previous.frame:
            raise AutomationError(
                f"track '{track}': duplicate automation timestamp at frame {current.frame}"
            )
    return AutomationCurve(tuple(ordered))


def parse_automation(raw: Sequence[Mapping[str, Any]] | None, track: str) -> AutomationCurve:
    """Parse a manifest ``automation`` array into a validated curve."""
    if raw is None:
        return default_curve()
    if isinstance(raw, (str, bytes)) or not isinstance(raw, Sequence):
        raise AutomationError(f"track '{track}': 'automation' must be an array")
    points = [_parse_point(item, track, index) for index, item in enumerate(raw)]
    return validate_points(points, track)


def describe_curve(curve: AutomationCurve) -> str:
    """Short human-readable summary used by the terminal report."""
    return f"{len(curve.points)} {_UNIT_LABELS[curve.unit]} point(s)"
