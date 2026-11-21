"""Project model: manifest validation, path checks and the shared frame clock."""

from __future__ import annotations

import math
import os
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

TARGET_SAMPLE_RATE = 48000
TARGET_CHANNELS = 2
MAX_TRACKS = 32
MAX_DURATION_SECONDS = 2 * 60 * 60
MAX_DURATION_FRAMES = MAX_DURATION_SECONDS * TARGET_SAMPLE_RATE
CONSISTENCY_TOLERANCE = 1e-6
# Bounds that still allow a dying whisper or full-scale safety attenuation
# while keeping linear gain finite (float64) and mixes meaningful.
MAX_GAIN_DB = 120.0
MIN_GAIN_DB = -120.0

DEFAULT_CHUNK_FRAMES = 1024


class ManifestError(ValueError):
    """Raised when a manifest is structurally or semantically invalid."""

    def __init__(self, message: str, track: str | None = None, field_name: str | None = None):
        self.track = track
        self.field_name = field_name
        prefix = ""
        if track is not None:
            prefix += f"track {track!r}"
        if field_name is not None:
            prefix += (": " if prefix else "") + f"field {field_name!r}"
        super().__init__(f"{prefix}: {message}" if prefix else message)


@dataclass(frozen=True)
class TrackSpec:
    name: str
    path: str
    gain_db: float = 0.0
    pan: float = 0.0
    automation: tuple[tuple[float, float], ...] = ()


@dataclass(frozen=True)
class Project:
    sample_rate: int
    channels: int
    tracks: tuple[TrackSpec, ...] = field(default_factory=tuple)


def _finite_number(value: Any, *, track: str | None = None, field_name: str | None = None) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ManifestError("must be a finite number", track, field_name)
    number = float(value)
    if not math.isfinite(number):
        raise ManifestError("must be a finite number", track, field_name)
    return number


def _require(mapping: dict, key: str, *, track: str | None = None, where: str = "manifest") -> Any:
    if key not in mapping:
        raise ManifestError(f"missing in {where}", track, key)
    return mapping[key]


def _gain_db(value: Any, *, track: str, field_name: str) -> float:
    db = _finite_number(value, track=track, field_name=field_name)
    if not MIN_GAIN_DB <= db <= MAX_GAIN_DB:
        raise ManifestError(
            f"must be within [{MIN_GAIN_DB:g}, {MAX_GAIN_DB:g}] dB", track, field_name
        )
    return db


def _parse_track(raw: Any, index: int) -> TrackSpec:
    if not isinstance(raw, dict):
        raise ManifestError(f"track #{index} must be an object", f"#{index}")
    name_raw = _require(raw, "name", where=f"track #{index}")
    if not isinstance(name_raw, str) or not name_raw.strip():
        raise ManifestError("must be a non-empty string", f"#{index}", "name")
    name = name_raw.strip()

    path_raw = _require(raw, "path", track=name)
    if not isinstance(path_raw, str) or not path_raw.strip():
        raise ManifestError("must be a non-empty string", name, "path")
    path = path_raw.strip()

    gain_db = _gain_db(raw.get("gain_db", 0.0), track=name, field_name="gain_db")
    pan = _finite_number(raw.get("pan", 0.0), track=name, field_name="pan")
    if not -1.0 <= pan <= 1.0:
        raise ManifestError("must be within [-1.0, 1.0]", name, "pan")

    automation_raw = raw.get("automation", [])
    if automation_raw is None:
        automation_raw = []
    if not isinstance(automation_raw, list):
        raise ManifestError("must be a list of points", name, "automation")
    points: list[tuple[float, float]] = []
    for point_index, point in enumerate(automation_raw):
        where = f"automation point #{point_index}"
        if not isinstance(point, dict):
            raise ManifestError(f"{where} must be an object", name, "automation")
        t_raw = _require(point, "t", track=name, where=where)
        db_raw = _require(point, "gain_db", track=name, where=where)
        t = _finite_number(t_raw, track=name, field_name="automation.t")
        db = _gain_db(db_raw, track=name, field_name="automation.gain_db")
        if t < 0.0:
            raise ManifestError(f"{where} t must be >= 0", name, "automation.t")
        if points and t <= points[-1][0]:
            raise ManifestError(
                f"{where} t must be strictly increasing ({t} <= {points[-1][0]})",
                name,
                "automation.t",
            )
        points.append((t, db))

    return TrackSpec(name=name, path=path, gain_db=gain_db, pan=pan, automation=tuple(points))


def parse_manifest(raw: Any) -> Project:
    """Validate a decoded JSON manifest and return a :class:`Project`."""
    if not isinstance(raw, dict):
        raise ManifestError("manifest must be a JSON object")

    sample_rate_raw = _require(raw, "sample_rate")
    if isinstance(sample_rate_raw, bool) or not isinstance(sample_rate_raw, int):
        raise ManifestError("must be an integer", field_name="sample_rate")
    if sample_rate_raw != TARGET_SAMPLE_RATE:
        raise ManifestError(
            f"must be {TARGET_SAMPLE_RATE} (target output rate)", field_name="sample_rate"
        )

    channels_raw = _require(raw, "channels")
    if isinstance(channels_raw, bool) or not isinstance(channels_raw, int):
        raise ManifestError("must be an integer", field_name="channels")
    if channels_raw != TARGET_CHANNELS:
        raise ManifestError(f"must be {TARGET_CHANNELS}", field_name="channels")

    tracks_raw = _require(raw, "tracks")
    if not isinstance(tracks_raw, list):
        raise ManifestError("must be a list", field_name="tracks")
    if not tracks_raw:
        raise ManifestError("must contain at least one track", field_name="tracks")
    if len(tracks_raw) > MAX_TRACKS:
        raise ManifestError(f"at most {MAX_TRACKS} tracks are supported", field_name="tracks")

    tracks: list[TrackSpec] = []
    seen: set[str] = set()
    for index, raw_track in enumerate(tracks_raw):
        track = _parse_track(raw_track, index)
        if track.name in seen:
            raise ManifestError("duplicate track name", track.name, "name")
        seen.add(track.name)
        tracks.append(track)

    return Project(sample_rate=sample_rate_raw, channels=channels_raw, tracks=tuple(tracks))


def resolve_track_paths(project: Project, base_dir: str | os.PathLike[str]) -> list[Path]:
    """Resolve and validate every source path referenced by the manifest."""
    base = Path(base_dir)
    resolved: list[Path] = []
    for track in project.tracks:
        candidate = Path(track.path)
        path = candidate if candidate.is_absolute() else base / candidate
        if not path.exists():
            raise ManifestError(f"file not found: {path}", track.name, "path")
        if not path.is_file():
            raise ManifestError(f"not a regular file: {path}", track.name, "path")
        if not os.access(path, os.R_OK):
            raise ManifestError(f"file is not readable: {path}", track.name, "path")
        resolved.append(path)
    return resolved


def resampled_frames(frames_in: int, rate_in: int, rate_out: int = TARGET_SAMPLE_RATE) -> int:
    """Output frame count for a resample: ``ceil(frames_in * rate_out / rate_in)``."""
    if frames_in < 0 or rate_in <= 0 or rate_out <= 0:
        raise ValueError("invalid frame or sample-rate value")
    return -((-frames_in * rate_out) // rate_in)


def check_duration(frames_in: int, rate_in: int, track: str = "") -> None:
    """Reject sources longer than the supported two-hour project ceiling."""
    if frames_in <= 0:
        raise ManifestError("source contains no audio frames", track or None, "path")
    if rate_in <= 0:
        raise ManifestError("invalid sample rate in source file", track or None, "path")
    duration = frames_in / rate_in
    if duration > MAX_DURATION_SECONDS:
        raise ManifestError(
            f"source is {duration:.3f}s long, exceeding the {MAX_DURATION_SECONDS}s limit",
            track or None,
            "path",
        )
    if resampled_frames(frames_in, rate_in) > MAX_DURATION_FRAMES:
        raise ManifestError("resampled length exceeds the two-hour limit", track or None, "path")
