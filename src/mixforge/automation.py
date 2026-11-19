"""Deterministic gain automation and fade evaluation."""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np

from .errors import ConfigError
from .model import AutomationSpec, Project, Track


def db_to_linear(db: float | np.ndarray) -> float | np.ndarray:
    """Convert decibels to a linear amplitude without hiding overflow."""

    return np.power(10.0, np.asarray(db, dtype=np.float64) / 20.0)


@dataclass(frozen=True)
class AutomationCurve:
    """Piecewise automation sampled on the project timeline.

    Gains interpolate in the decibel domain, which keeps equal gain steps
    perceptually even.  ``step`` holds the previous keyframe value.  Positions
    before the first / after the last keyframe hold the endpoint values.
    """

    interpolation: str
    frames: np.ndarray
    gains_db: np.ndarray

    @classmethod
    def from_spec(cls, spec: AutomationSpec, *, location: str = "automation") -> "AutomationCurve":
        if not spec.points:
            raise ConfigError("automation needs at least one keyframe", location=location)
        frames = np.fromiter((p.frame for p in spec.points), dtype=np.int64)
        gains = np.fromiter((p.gain_db for p in spec.points), dtype=np.float64)
        if frames.ndim != 1 or frames.size == 0:
            raise ConfigError("automation needs at least one keyframe", location=location)
        if np.any(frames < 0):
            raise ConfigError("keyframe frames must be non-negative", location=location)
        if np.any(np.diff(frames) <= 0):
            raise ConfigError(
                "keyframe coordinates must be unique and strictly increasing", location=location
            )
        if not np.all(np.isfinite(gains)):
            raise ConfigError("keyframe gains must be finite", location=location)
        if spec.interpolation not in {"linear", "step"}:
            raise ConfigError("interpolation must be linear or step", location=location)
        return cls(interpolation=spec.interpolation, frames=frames, gains_db=gains)

    def gain_db_at(self, positions: np.ndarray) -> np.ndarray:
        pos = np.asarray(positions, dtype=np.int64)
        if pos.size == 0:
            return np.empty(0, dtype=np.float64)
        if pos.ndim != 1:
            raise ConfigError("automation positions must be one-dimensional")
        # searchsorted(..., right) - 1 gives the keyframe at or before pos.
        index = np.searchsorted(self.frames, pos, side="right") - 1
        left = np.clip(index, 0, self.frames.size - 1)
        right = np.clip(index + 1, 0, self.frames.size - 1)
        if self.interpolation == "step":
            return self.gains_db[left].astype(np.float64, copy=True)
        span = (self.frames[right] - self.frames[left]).astype(np.float64)
        fraction = np.divide(
            (pos - self.frames[left]).astype(np.float64),
            span,
            out=np.zeros_like(span),
            where=span > 0.0,
        )
        return self.gains_db[left] + fraction * (self.gains_db[right] - self.gains_db[left])


def effective_gain_linear(
    track: Track,
    curve: AutomationCurve | None,
    project_frames: np.ndarray,
) -> np.ndarray:
    """Track gain (dB) combined with automation at absolute project frames."""

    static_db = np.float64(track.gain_db)
    if curve is None:
        return np.full(project_frames.size, float(db_to_linear(static_db)), dtype=np.float64)
    return np.asarray(db_to_linear(curve.gain_db_at(project_frames) + static_db), dtype=np.float64)


def fade_amplitude(
    total_frames: int,
    fade_in_frames: int,
    fade_out_frames: int,
    track_positions: np.ndarray,
    *,
    location: str = "fade",
) -> np.ndarray:
    """Linear clip-local fade envelope, measured in source frames."""

    if fade_in_frames < 0 or fade_out_frames < 0:
        raise ConfigError("fade durations must be non-negative", location=location)
    if fade_in_frames + fade_out_frames > total_frames:
        raise ConfigError(
            "fade durations exceed the track length",
            location=location,
            context={
                "track_frames": total_frames,
                "fade_in": fade_in_frames,
                "fade_out": fade_out_frames,
            },
        )
    pos = np.asarray(track_positions, dtype=np.int64)
    amplitude = np.ones(pos.shape, dtype=np.float64)
    if fade_in_frames > 0:
        mask = (pos >= 0) & (pos < fade_in_frames)
        amplitude[mask] = pos[mask].astype(np.float64) / float(fade_in_frames)
    if fade_out_frames > 0:
        start = total_frames - fade_out_frames
        mask = pos >= start
        amplitude[mask] = (
            (total_frames - 1 - pos[mask]).astype(np.float64) / float(fade_out_frames)
        )
    return np.clip(amplitude, 0.0, 1.0)


def _add_role(roles: dict[int, set[str]], frame: int, role: str, limit: int) -> None:
    if 0 <= frame < limit:
        roles.setdefault(frame, set()).add(role)


def automation_samples(project: Project, block_size: int) -> list[dict[str, object]]:
    """Sample automation around keyframes and block boundaries.

    Returning both exact coordinates and their immediate neighbours makes it
    possible to verify continuity across a stream block split without
    re-rendering the project.
    """

    if block_size <= 0:
        raise ConfigError("block_size must be positive", location="block_size")
    frames = project.frames
    samples: list[dict[str, object]] = []
    if frames == 0:
        return samples

    for track in project.tracks:
        if track.automation is None:
            continue
        curve = AutomationCurve.from_spec(
            track.automation, location=f"track[{track.id}].automation"
        )
        roles: dict[int, set[str]] = {}
        for point in track.automation.points:
            _add_role(roles, point.frame, "keyframe", frames)
            _add_role(roles, point.frame - 1, "keyframe_neighbor", frames)
            _add_role(roles, point.frame + 1, "keyframe_neighbor", frames)
        for boundary in range(0, frames, block_size):
            _add_role(roles, boundary, "block_boundary", frames)
            _add_role(roles, boundary - 1, "block_boundary_neighbor", frames)
            _add_role(roles, boundary + 1, "block_boundary_neighbor", frames)
        if project.loop is not None:
            for edge in (
                project.loop.start_frame,
                project.loop.end_frame,
                project.loop.expanded_end,
            ):
                _add_role(roles, edge, "loop_boundary", frames)
                _add_role(roles, edge - 1, "loop_boundary_neighbor", frames)
                _add_role(roles, edge + 1, "loop_boundary_neighbor", frames)
        _add_role(roles, 0, "endpoint", frames)
        _add_role(roles, frames - 1, "endpoint", frames)

        coordinates = np.fromiter(sorted(roles), dtype=np.int64)
        gains = curve.gain_db_at(coordinates)
        for frame, gain_db in zip(coordinates, gains):
            samples.append(
                {
                    "track": track.id,
                    "frame": int(frame),
                    "gain_db": float(gain_db),
                    "effective_gain_db": float(gain_db + track.gain_db),
                    "roles": sorted(roles[int(frame)]),
                }
            )
    return samples
