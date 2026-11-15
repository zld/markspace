"""Project model: parse and validate JSON mixing projects.

A project describes the target sample rate, total length in frames and the
list of tracks (source file, start frame, gain automation).  Validation is
strict: any malformed value raises :class:`ConfigError` with a precise reason
so the CLI can report it without producing a misleading success report.
"""

from __future__ import annotations

import json
import math
from dataclasses import dataclass, field
from pathlib import Path

from . import (
    MAX_BLOCK_FRAMES,
    MAX_SAMPLE_RATE,
    MAX_TRACKS,
    MIN_BLOCK_FRAMES,
    MIN_SAMPLE_RATE,
    ConfigError,
)

HOLD = "hold"
LINEAR = "linear"
_GAIN_MODES = (HOLD, LINEAR)


@dataclass(frozen=True)
class GainPoint:
    """A single automation point at an absolute project frame."""

    frame: int
    gain: float
    mode: str


@dataclass(frozen=True)
class TrackConfig:
    track_id: str
    path: str
    start_frame: int
    gain: float
    automation: tuple[GainPoint, ...] = field(default_factory=tuple)


@dataclass(frozen=True)
class Project:
    sample_rate: int
    frames: int
    tracks: tuple[TrackConfig, ...]
    block_frames: int | None = None
    automation_sample_frames: tuple[int, ...] = ()


def _require_mapping(obj, where: str) -> dict:
    if not isinstance(obj, dict):
        raise ConfigError(f"{where} must be a JSON object")
    return obj


def _int(value, name: str) -> int:
    # bool is a subclass of int; reject it explicitly.
    if isinstance(value, bool) or not isinstance(value, int):
        raise ConfigError(f"{name} must be an integer, got {type(value).__name__}")
    return value


def _finite_float(value, name: str) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ConfigError(f"{name} must be a number, got {type(value).__name__}")
    fv = float(value)
    if not math.isfinite(fv):
        raise ConfigError(f"{name} must be finite")
    return fv


def _validate_automation(raw, where: str) -> tuple[GainPoint, ...]:
    if not isinstance(raw, list):
        raise ConfigError(f"{where}.automation must be a list")
    points: list[GainPoint] = []
    last_frame = -1
    for i, item in enumerate(raw):
        obj = _require_mapping(item, f"{where}.automation[{i}]")
        frame = _int(obj.get("frame"), f"{where}.automation[{i}].frame")
        if frame < 0:
            raise ConfigError(f"{where}.automation[{i}].frame must be >= 0")
        if frame <= last_frame:
            raise ConfigError(
                f"{where}.automation[{i}].frame={frame} is out of order or duplicated"
            )
        gain = _finite_float(obj.get("gain"), f"{where}.automation[{i}].gain")
        mode = obj.get("mode", HOLD)
        if mode not in _GAIN_MODES:
            raise ConfigError(
                f"{where}.automation[{i}].mode must be one of {_GAIN_MODES}"
            )
        points.append(GainPoint(frame=frame, gain=gain, mode=mode))
        last_frame = frame
    return tuple(points)


def _validate_track(raw, index: int) -> TrackConfig:
    where = f"tracks[{index}]"
    obj = _require_mapping(raw, where)
    track_id = obj.get("id")
    if not isinstance(track_id, str) or not track_id:
        raise ConfigError(f"{where}.id must be a non-empty string")
    path = obj.get("path")
    if not isinstance(path, str) or not path:
        raise ConfigError(f"{where} (id={track_id!r}).path must be a non-empty string")
    start_frame = _int(obj.get("start_frame", 0), f"{where}.start_frame")
    if start_frame < 0:
        raise ConfigError(f"{where}.start_frame must be >= 0")
    gain = _finite_float(obj.get("gain", 1.0), f"{where}.gain")
    automation = _validate_automation(obj.get("automation", []), where)
    return TrackConfig(
        track_id=track_id,
        path=path,
        start_frame=start_frame,
        gain=gain,
        automation=automation,
    )


def validate_project(raw: dict) -> Project:
    """Validate a decoded JSON object and return an immutable :class:`Project`."""
    obj = _require_mapping(raw, "project")

    rate = _int(obj.get("sample_rate"), "sample_rate")
    if not MIN_SAMPLE_RATE <= rate <= MAX_SAMPLE_RATE:
        raise ConfigError(
            f"sample_rate must be within [{MIN_SAMPLE_RATE}, {MAX_SAMPLE_RATE}], got {rate}"
        )

    frames = _int(obj.get("frames"), "frames")
    if frames < 0:
        raise ConfigError("frames must be >= 0")

    raw_tracks = obj.get("tracks", [])
    if not isinstance(raw_tracks, list):
        raise ConfigError("tracks must be a list")
    if len(raw_tracks) > MAX_TRACKS:
        raise ConfigError(f"project exceeds the {MAX_TRACKS} track limit")

    seen_ids: set[str] = set()
    tracks: list[TrackConfig] = []
    for i, raw_track in enumerate(raw_tracks):
        track = _validate_track(raw_track, i)
        if track.track_id in seen_ids:
            raise ConfigError(f"duplicate track id: {track.track_id!r}")
        seen_ids.add(track.track_id)
        tracks.append(track)

    block_frames = obj.get("block_frames")
    if block_frames is not None:
        block_frames = _int(block_frames, "block_frames")
        if not MIN_BLOCK_FRAMES <= block_frames <= MAX_BLOCK_FRAMES:
            raise ConfigError(
                f"block_frames must be within [{MIN_BLOCK_FRAMES}, {MAX_BLOCK_FRAMES}]"
            )

    sample_frames = obj.get("automation_sample_frames", [])
    if not isinstance(sample_frames, list):
        raise ConfigError("automation_sample_frames must be a list")
    checked: list[int] = []
    for i, value in enumerate(sample_frames):
        f = _int(value, f"automation_sample_frames[{i}]")
        if f < 0 or f >= frames:
            raise ConfigError(
                f"automation_sample_frames[{i}]={f} outside project [0, {frames})"
            )
        checked.append(f)

    return Project(
        sample_rate=rate,
        frames=frames,
        tracks=tuple(tracks),
        block_frames=block_frames,
        automation_sample_frames=tuple(checked),
    )


def load_project(path: str | Path) -> Project:
    """Read a project JSON file from disk and validate it."""
    p = Path(path)
    try:
        text = p.read_text(encoding="utf-8")
    except OSError as exc:
        raise ConfigError(f"cannot read project file {p}: {exc}") from exc
    try:
        raw = json.loads(text)
    except json.JSONDecodeError as exc:
        raise ConfigError(f"invalid JSON in project file {p}: {exc}") from exc
    return validate_project(raw)
