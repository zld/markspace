"""Gain automation curves.

Gains are authored in dB.  Between control points the *dB* value is
linearly interpolated, then converted to a linear gain factor at sample
time.  Before the first point the first value is held; after the last
point the last value is held (hold-and-extend semantics).  An empty point
list means constant unity gain (0 dB).

All addressing uses absolute frame indices on the *output* timeline, so
streaming blocks never reset a curve at a block boundary.
"""

from __future__ import annotations

import math
from collections.abc import Sequence

import numpy as np

from .errors import ManifestError

DB_FLOOR = -1200.0
"""Sentinel dB value used where a linear gain is exactly zero."""


def db_to_linear(db: float | np.ndarray) -> float | np.ndarray:
    """Convert dB gain to a linear amplitude factor."""
    return np.power(10.0, np.asarray(db, dtype=np.float64) / 20.0)


class GainCurve:
    """Piecewise-linear-in-dB gain curve with hold-and-extend ends."""

    def __init__(self, points: Sequence[tuple[int, float]]):
        # ``points`` must already be validated; use :meth:`from_manifest`.
        self.points: list[tuple[int, float]] = [(int(f), float(g)) for f, g in points]
        if self.points:
            self._frames = np.asarray([p[0] for p in self.points], dtype=np.int64)
            self._gains = np.asarray([p[1] for p in self.points], dtype=np.float64)
        else:
            self._frames = np.empty(0, dtype=np.int64)
            self._gains = np.empty(0, dtype=np.float64)

    # ------------------------------------------------------------------ #
    # Construction / validation
    # ------------------------------------------------------------------ #
    @classmethod
    def from_manifest(
        cls,
        raw: object,
        total_frames: int,
        track_id: str,
    ) -> "GainCurve":
        """Parse the ``automation`` field of one manifest track.

        Raises :class:`ManifestError` naming the track and field on any
        problem: non-finite gain, non-integral / out-of-range frame,
        duplicate or backwards frames.
        """
        if raw is None:
            return cls([])
        if not isinstance(raw, list):
            raise ManifestError(
                f"track {track_id!r} field 'automation' must be a list of "
                f"{{frame, gain_db}} points"
            )
        points: list[tuple[int, float]] = []
        prev_frame: int | None = None
        for index, entry in enumerate(raw):
            field_prefix = f"track {track_id!r} automation[{index}]"
            if not isinstance(entry, dict):
                raise ManifestError(f"{field_prefix} must be an object")
            if "frame" not in entry or "gain_db" not in entry:
                raise ManifestError(
                    f"{field_prefix} requires fields 'frame' and 'gain_db'"
                )
            frame = _validated_frame(entry["frame"], total_frames, field_prefix)
            gain = entry["gain_db"]
            if isinstance(gain, bool) or not isinstance(gain, (int, float)):
                raise ManifestError(f"{field_prefix}.gain_db must be a number")
            gain_f = float(gain)
            if not math.isfinite(gain_f):
                raise ManifestError(
                    f"{field_prefix}.gain_db must be finite (NaN/Inf rejected)"
                )
            if prev_frame is not None and frame <= prev_frame:
                raise ManifestError(
                    f"{field_prefix}.frame={frame} duplicates or goes "
                    f"backwards (previous frame {prev_frame}); control "
                    f"points must be strictly increasing"
                )
            points.append((frame, gain_f))
            prev_frame = frame
        return cls(points)

    # ------------------------------------------------------------------ #
    # Sampling
    # ------------------------------------------------------------------ #
    def sample_db(self, frames: np.ndarray) -> np.ndarray:
        """Return the dB curve sampled at absolute output ``frames``."""
        frames = np.asarray(frames, dtype=np.int64)
        if not self.points:
            return np.zeros(frames.shape, dtype=np.float64)
        # np.interp gives constant extension outside the endpoint range,
        # which is exactly the hold-first / hold-last semantics we need.
        return np.interp(frames, self._frames, self._gains)

    def sample_linear(self, frames: np.ndarray) -> np.ndarray:
        """Return linear gain factors at absolute output ``frames``."""
        db = self.sample_db(frames)
        linear = np.power(10.0, db / 20.0)
        # A -infinite dB point (mute) maps to exactly zero instead of
        # relying on the floating-point limit of 10**(-x).
        linear[db <= DB_FLOOR] = 0.0
        return linear

    def __repr__(self) -> str:  # pragma: no cover - debugging aid
        return f"GainCurve({self.points!r})"


def _validated_frame(value: object, total_frames: int, field_prefix: str) -> int:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ManifestError(f"{field_prefix}.frame must be an integer frame index")
    if isinstance(value, float):
        if not math.isfinite(value) or not float(value).is_integer():
            raise ManifestError(f"{field_prefix}.frame must be a whole number")
        frame = int(value)
    else:
        frame = int(value)
    if frame < 0 or frame > total_frames:
        raise ManifestError(
            f"{field_prefix}.frame={frame} is outside the timeline "
            f"[0, {total_frames}]"
        )
    return frame
