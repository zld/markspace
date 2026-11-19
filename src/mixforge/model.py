"""Project JSON parsing and validation.

Every failure carries a dotted location such as ``tracks[2].automation`` so
the terminal error can point at the offending field.
"""

from __future__ import annotations

import math
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from .errors import ConfigError
from .schema import (
    MAX_FRAME,
    boolean,
    fail,
    frame_field,
    gain_db,
    integer,
    mapping,
    number,
    seconds_to_frames,
    string,
)


@dataclass(frozen=True)
class GainPoint:
    frame: int
    gain_db: float


@dataclass(frozen=True)
class AutomationSpec:
    interpolation: str
    points: tuple[GainPoint, ...]


@dataclass(frozen=True)
class FadeSpec:
    in_frames: int = 0
    out_frames: int = 0


@dataclass(frozen=True)
class Track:
    id: str
    path: str
    bus: str | None
    start_frame: int
    gain_db: float
    mute: bool
    automation: AutomationSpec | None
    fade: FadeSpec


@dataclass(frozen=True)
class Bus:
    id: str
    gain_db: float
    mute: bool


@dataclass(frozen=True)
class Master:
    gain_db: float
    limiter: bool
    ceiling: float


@dataclass(frozen=True)
class LoopRegion:
    start_frame: int
    end_frame: int
    count: int

    @property
    def span(self) -> int:
        return self.end_frame - self.start_frame

    @property
    def expanded_end(self) -> int:
        return self.start_frame + self.span * self.count


@dataclass(frozen=True)
class Project:
    sample_rate: int
    frames: int
    channels: int
    tracks: tuple[Track, ...]
    buses: tuple[Bus, ...]
    master: Master
    loop: LoopRegion | None
    source_dir: Path
    source: str


def _parse_automation(raw: Any, sample_rate: int, location: str) -> AutomationSpec | None:
    if raw is None:
        return None
    obj = mapping(raw, location)
    unit = string(obj, "unit", location, default="seconds")
    if unit not in {"seconds", "frames"}:
        fail(f"{location}.unit", "expected 'seconds' or 'frames'")
    interpolation = string(obj, "interpolation", location, default="linear")
    if interpolation not in {"linear", "step"}:
        fail(f"{location}.interpolation", "expected 'linear' or 'step'")
    points_raw = obj.get("points")
    if not isinstance(points_raw, list) or not points_raw:
        fail(f"{location}.points", "expected a non-empty array of keyframes")

    points: list[GainPoint] = []
    seen: dict[int, int] = {}
    for index, item in enumerate(points_raw):
        point_loc = f"{location}.points[{index}]"
        point = mapping(item, point_loc)
        if "gain_db" not in point:
            fail(f"{point_loc}.gain_db", "missing gain_db")
        value = gain_db(point, "gain_db", point_loc)
        if "time" not in point:
            fail(f"{point_loc}.time", "missing time")
        time_value = point["time"]
        if unit == "seconds":
            if isinstance(time_value, bool) or not isinstance(time_value, (int, float)):
                fail(f"{point_loc}.time", "expected a number of seconds")
            seconds = float(time_value)
            if not math.isfinite(seconds) or seconds < 0:
                fail(f"{point_loc}.time", "expected finite non-negative seconds")
            frame = seconds_to_frames(seconds, sample_rate)
        else:
            if isinstance(time_value, bool) or not isinstance(time_value, int):
                fail(f"{point_loc}.time", "expected an integer frame")
            frame = time_value
            if frame < 0:
                fail(f"{point_loc}.time", "frame must be non-negative")
        if frame > MAX_FRAME:
            fail(f"{point_loc}.time", f"frame exceeds the supported maximum {MAX_FRAME}")
        if frame in seen:
            first = seen[frame]
            raise ConfigError(
                f"duplicate keyframe coordinate {frame} (also points[{first}])",
                location=f"{point_loc}.time",
                context={"frame": frame, "first_index": first},
            )
        seen[frame] = index
        points.append(GainPoint(frame=frame, gain_db=value))

    points.sort(key=lambda p: p.frame)
    return AutomationSpec(interpolation=interpolation, points=tuple(points))


def _parse_fade(raw: Any, sample_rate: int, location: str) -> FadeSpec:
    if raw is None:
        return FadeSpec()
    obj = mapping(raw, location)
    in_frames = frame_field(obj, "in_frames", "in_seconds", sample_rate, f"{location}.in", default=0)
    out_frames = frame_field(
        obj, "out_frames", "out_seconds", sample_rate, f"{location}.out", default=0
    )
    return FadeSpec(in_frames=in_frames, out_frames=out_frames)


def _parse_master(raw: Any) -> Master:
    if raw is None:
        return Master(gain_db=0.0, limiter=False, ceiling=1.0)
    obj = mapping(raw, "master")
    master_gain = gain_db(obj, "gain_db", "master", default=0.0)
    limiter_raw = obj.get("limiter", False)
    ceiling = 1.0
    if isinstance(limiter_raw, dict):
        limiter_obj = mapping(limiter_raw, "master.limiter")
        limiter = boolean(limiter_obj, "enabled", "master.limiter", default=True)
        ceiling = number(limiter_obj, "ceiling", "master.limiter", default=1.0)
    elif isinstance(limiter_raw, bool):
        limiter = limiter_raw
        ceiling = number(obj, "ceiling", "master", default=1.0)
    else:
        fail("master.limiter", "expected true, false, or an object")
    if not 0.0 < ceiling <= 1.0:
        fail("master.ceiling", "expected a value greater than 0 and at most 1")
    return Master(gain_db=master_gain, limiter=limiter, ceiling=ceiling)


def _parse_loop(raw: Any, sample_rate: int, project_frames: int) -> LoopRegion | None:
    if raw is None:
        return None
    obj = mapping(raw, "loop")
    start = frame_field(obj, "start_frame", "start_seconds", sample_rate, "loop.start", default=None)
    end = frame_field(obj, "end_frame", "end_seconds", sample_rate, "loop.end", default=None)
    count = integer(obj, "count", "loop")
    if start >= end:
        fail("loop", "loop start must be before loop end")
    if end > project_frames:
        fail("loop.end", "loop range must fall inside the project duration")
    if count < 1:
        fail("loop.count", "must be at least 1")
    if start + (end - start) * count > project_frames:
        fail("loop.count", "repeated loop region exceeds the project duration")
    return LoopRegion(start_frame=start, end_frame=end, count=count)


def parse_project(data: Any, *, source_dir: Path | str, source: str = "<project>") -> Project:
    """Validate a decoded project document and return an immutable model."""

    obj = mapping(data, source)
    if "sample_rate" not in obj:
        fail("sample_rate", "missing required field")
    sample_rate = integer(obj, "sample_rate", "project")
    if sample_rate <= 0:
        fail("sample_rate", "expected a positive integer")
    channels = integer(obj, "channels", "project", default=2)
    if channels not in (1, 2):
        fail("channels", "expected 1 or 2")

    has_frames = "duration_frames" in obj
    has_seconds = "duration_seconds" in obj
    if has_frames == has_seconds:
        fail("duration", "declare exactly one of duration_frames or duration_seconds")
    if has_frames:
        frames = integer(obj, "duration_frames", "project")
    else:
        seconds = number(obj, "duration_seconds", "project")
        if seconds < 0:
            fail("duration_seconds", "must be non-negative")
        frames = seconds_to_frames(seconds, sample_rate)
    if frames < 0:
        fail("duration_frames", "must be non-negative")
    if frames > MAX_FRAME:
        fail("duration_frames", f"duration exceeds the supported maximum {MAX_FRAME} frames")

    tracks_raw = obj.get("tracks")
    if not isinstance(tracks_raw, list):
        fail("tracks", "expected an array")
    buses_raw = obj.get("buses", [])
    if not isinstance(buses_raw, list):
        fail("buses", "expected an array")

    buses: list[Bus] = []
    bus_ids: set[str] = set()
    for index, item in enumerate(buses_raw):
        loc = f"buses[{index}]"
        bus_obj = mapping(item, loc)
        bus_id = string(bus_obj, "id", loc)
        if bus_id == "master":
            fail(f"{loc}.id", "'master' is reserved")
        if bus_id in bus_ids:
            raise ConfigError(
                f"duplicate bus id {bus_id!r}", location=f"{loc}.id", context={"id": bus_id}
            )
        bus_ids.add(bus_id)
        buses.append(
            Bus(
                id=bus_id,
                gain_db=gain_db(bus_obj, "gain_db", loc, default=0.0),
                mute=boolean(bus_obj, "mute", loc, default=False),
            )
        )

    tracks: list[Track] = []
    track_ids: set[str] = set()
    for index, item in enumerate(tracks_raw):
        loc = f"tracks[{index}]"
        track_obj = mapping(item, loc)
        track_id = string(track_obj, "id", loc)
        if track_id in track_ids:
            raise ConfigError(
                f"duplicate track id {track_id!r}", location=f"{loc}.id", context={"id": track_id}
            )
        track_ids.add(track_id)
        path = string(track_obj, "path", loc)
        bus = track_obj.get("bus")
        if bus is not None and (not isinstance(bus, str) or bus not in bus_ids):
            fail(f"{loc}.bus", f"unknown bus {bus!r}")
        tracks.append(
            Track(
                id=track_id,
                path=path,
                bus=bus,
                start_frame=frame_field(
                    track_obj, "start_frame", "start_seconds", sample_rate, f"{loc}.start", default=0
                ),
                gain_db=gain_db(track_obj, "gain_db", loc, default=0.0),
                mute=boolean(track_obj, "mute", loc, default=False),
                automation=_parse_automation(
                    track_obj.get("automation"), sample_rate, f"{loc}.automation"
                ),
                fade=_parse_fade(track_obj.get("fade"), sample_rate, f"{loc}.fade"),
            )
        )

    return Project(
        sample_rate=sample_rate,
        frames=frames,
        channels=channels,
        tracks=tuple(tracks),
        buses=tuple(buses),
        master=_parse_master(obj.get("master")),
        loop=_parse_loop(obj.get("loop"), sample_rate, frames),
        source_dir=Path(source_dir),
        source=source,
    )
