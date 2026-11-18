"""Domain model: tracks, timeline, automation data and manifest validation.

The manifest is a JSON document with the form::

    {
      "output_rate": 48000,            # optional, overridden by CLI --rate
      "channels": 2,                  # optional, default 2
      "bus_gain": 1.0,                # optional linear gain, default 1.0
      "tracks": [
        {
          "name": "voice",
          "file": "voice.wav",        # relative to manifest directory
          "start": 0.0,               # start time in seconds, >= 0
          "gain": 1.0,                # linear track gain, must be finite, >= 0
          "muted": false,             # optional
          "channel_map": [0, 1],      # optional; source channel per output ch
          "automation": {
            "mode": "linear",         # "linear" or "db"
            "points": [{"time": 0.0, "value": 1.0}, ...]
          }
        }
      ]
    }

Validation errors carry the offending track name and field so callers can
locate problems programmatically.
"""
from __future__ import annotations

import json
import math
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any


class AudioMixError(Exception):
    """Base error. ``code`` maps to the CLI process exit code."""

    code = 3


class ManifestError(AudioMixError):
    """Manifest or argument validation error (exit code 2)."""

    code = 2

    def __init__(self, message: str, track: str | None = None,
                 field_name: str | None = None):
        super().__init__(message)
        self.track = track
        self.field_name = field_name

    def as_dict(self) -> dict[str, Any]:
        out: dict[str, Any] = {"error": str(self)}
        if self.track is not None:
            out["track"] = self.track
        if self.field_name is not None:
            out["field"] = self.field_name
        return out


class RenderError(AudioMixError):
    """Input/output or rendering failure (exit code 3)."""

    code = 3

    def __init__(self, message: str, track: str | None = None):
        super().__init__(message)
        self.track = track

    def as_dict(self) -> dict[str, Any]:
        out: dict[str, Any] = {"error": str(self)}
        if self.track is not None:
            out["track"] = self.track
        return out


AUTOMATION_MODES = ("linear", "db")


@dataclass(frozen=True)
class AutomationPoint:
    """One automation breakpoint.

    For ``linear`` mode *value* is a linear gain (>= 0). For ``db`` mode
    *value* is a gain in dB; negative infinity is written as ``null``.
    """

    time: float
    value: float | None  # None means -inf dB in "db" mode
    mode: str

    def as_linear_gain(self) -> float:
        """Convert the point value to a linear gain."""
        if self.mode == "db":
            if self.value is None:
                return 0.0
            return 10.0 ** (self.value / 20.0)
        return float(self.value)  # type: ignore[arg-type]


@dataclass(frozen=True)
class AutomationSpec:
    mode: str
    points: tuple[AutomationPoint, ...] = field(default_factory=tuple)


@dataclass(frozen=True)
class TrackSpec:
    name: str
    file: Path
    start: float
    gain: float
    channel_map: tuple[int, ...] | None
    muted: bool
    automation: AutomationSpec | None


@dataclass(frozen=True)
class Timeline:
    tracks: tuple[TrackSpec, ...]
    output_rate: int
    channels: int
    bus_gain: float
    base_dir: Path


def _require_number(obj: dict[str, Any], key: str, track: str | None) -> float:
    if key not in obj:
        raise ManifestError(f"missing required field '{key}'", track, key)
    val = obj[key]
    if isinstance(val, bool) or not isinstance(val, (int, float)):
        raise ManifestError(f"field '{key}' must be a number, got {val!r}",
                            track, key)
    return float(val)


def _parse_automation(raw: Any, track: str) -> AutomationSpec:
    if not isinstance(raw, dict):
        raise ManifestError("automation must be an object", track,
                            "automation")
    mode = raw.get("mode", "linear")
    if mode not in AUTOMATION_MODES:
        raise ManifestError(
            f"automation mode must be one of {AUTOMATION_MODES}, got {mode!r}",
            track, "automation.mode")
    raw_points = raw.get("points", [])
    if not isinstance(raw_points, list) or not raw_points:
        raise ManifestError("automation points must be a non-empty list",
                            track, "automation.points")
    points: list[AutomationPoint] = []
    prev_time: float | None = None
    for idx, rp in enumerate(raw_points):
        fld = f"automation.points[{idx}]"
        if not isinstance(rp, dict):
            raise ManifestError("automation point must be an object", track,
                                fld)
        t = _require_number(rp, "time", track)
        if not math.isfinite(t):
            raise ManifestError("automation time must be finite", track,
                                f"{fld}.time")
        if prev_time is not None and t <= prev_time:
            raise ManifestError(
                "automation points must be strictly increasing in time; "
                f"time {t} follows {prev_time}", track, f"{fld}.time")
        prev_time = t
        value: float | None
        if mode == "db" and rp.get("value") is None:
            value = None  # explicit negative infinity dB
        else:
            value = _require_number(rp, "value", track)
            if not math.isfinite(value):
                raise ManifestError("automation value must be finite "
                                    "(use null for -inf dB)", track,
                                    f"{fld}.value")
            if mode == "linear" and value < 0.0:
                raise ManifestError("linear automation value must be >= 0",
                                    track, f"{fld}.value")
        points.append(AutomationPoint(t, value, mode))
    return AutomationSpec(mode, tuple(points))


def _parse_track(raw: Any, index: int, channels: int) -> TrackSpec:
    if not isinstance(raw, dict):
        raise ManifestError(f"track #{index} must be an object")
    name = raw.get("name")
    if not isinstance(name, str) or not name:
        raise ManifestError("track requires a non-empty string 'name'",
                            name if isinstance(name, str) else None, "name")
    fname = raw.get("file")
    if not isinstance(fname, str) or not fname:
        raise ManifestError("track requires a non-empty string 'file'", name,
                            "file")
    start = _require_number(raw, "start", name)
    if start < 0.0 or not math.isfinite(start):
        raise ManifestError(f"start must be a finite number >= 0, got {start}",
                            name, "start")
    gain = raw.get("gain", 1.0)
    if isinstance(gain, bool) or not isinstance(gain, (int, float)):
        raise ManifestError(f"gain must be a number, got {gain!r}", name,
                            "gain")
    gain = float(gain)
    if not math.isfinite(gain) or gain < 0.0:
        raise ManifestError(f"gain must be finite and >= 0, got {gain}", name,
                            "gain")
    muted = bool(raw.get("muted", False))
    channel_map = raw.get("channel_map")
    if channel_map is not None:
        if not isinstance(channel_map, list) or not channel_map:
            raise ManifestError("channel_map must be a non-empty list of "
                                "non-negative integers", name, "channel_map")
        if len(channel_map) != channels:
            raise ManifestError(
                f"channel_map length {len(channel_map)} does not match "
                f"output channel count {channels}", name, "channel_map")
        cm: list[int] = []
        for j, ch in enumerate(channel_map):
            if isinstance(ch, bool) or not isinstance(ch, int) or ch < 0:
                raise ManifestError(
                    f"channel_map[{j}] must be a non-negative integer", name,
                    "channel_map")
            cm.append(ch)
        channel_map = tuple(cm)
    automation = None
    if "automation" in raw:
        automation = _parse_automation(raw["automation"], name)
    return TrackSpec(name=name, file=Path(fname), start=start, gain=gain,
                     channel_map=channel_map, muted=muted,
                     automation=automation)


def parse_manifest(path: str | Path, output_rate: int | None = None,
                   channels: int | None = None) -> Timeline:
    """Load and validate a manifest, resolving paths relative to it."""
    path = Path(path)
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise RenderError(f"manifest not found: {path}") from exc
    except json.JSONDecodeError as exc:
        raise ManifestError(
            f"invalid JSON in manifest at line {exc.lineno} col {exc.colno}: "
            f"{exc.msg}") from exc
    if not isinstance(data, dict):
        raise ManifestError("manifest root must be a JSON object")
    raw_tracks = data.get("tracks")
    if not isinstance(raw_tracks, list) or not raw_tracks:
        raise ManifestError("manifest requires a non-empty 'tracks' list",
                            field_name="tracks")
    out_ch = int(channels if channels is not None
                 else data.get("channels", 2))
    if out_ch <= 0:
        raise ManifestError(f"channel count must be > 0, got {out_ch}",
                            field_name="channels")
    rate = int(output_rate if output_rate is not None
               else data.get("output_rate", 48000))
    if rate <= 0:
        raise ManifestError(f"output rate must be > 0, got {rate}",
                            field_name="output_rate")
    bus_gain = data.get("bus_gain", 1.0)
    if isinstance(bus_gain, bool) or not isinstance(bus_gain, (int, float)):
        raise ManifestError("bus_gain must be a number", field_name="bus_gain")
    bus_gain = float(bus_gain)
    if not math.isfinite(bus_gain) or bus_gain < 0.0:
        raise ManifestError("bus_gain must be finite and >= 0",
                            field_name="bus_gain")
    names: set[str] = set()
    tracks: list[TrackSpec] = []
    for idx, rt in enumerate(raw_tracks):
        track = _parse_track(rt, idx, out_ch)
        if track.name in names:
            raise ManifestError(f"duplicate track name {track.name!r}",
                                track.name, "name")
        names.add(track.name)
        tracks.append(track)
    return Timeline(tracks=tuple(tracks), output_rate=rate, channels=out_ch,
                    bus_gain=bus_gain, base_dir=path.parent.resolve())
