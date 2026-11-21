"""Manifest data model: tracks, buses and validation.

The manifest is a JSON document::

    {
      "buses": {"drums": {"target": "master"}, "master": {"target": null}},
      "tracks": [
        {"file": "kick.wav", "offset": 0.0, "bus": "drums",
         "curve": [[0.0, 0.0], [1.5, -6.0]]}
      ]
    }

Track file paths resolve relative to the manifest directory.  A bus whose
``target`` is null feeds the master output directly.  Every validation
failure raises :class:`ManifestError` carrying the track index (when
applicable) and a human readable reason.
"""

from __future__ import annotations

import json
import math
from dataclasses import dataclass, field
from pathlib import Path
from typing import Optional

MAX_TRACKS = 128
MAX_TRACK_SECONDS = 600.0  # 10 minutes per input file


class ManifestError(Exception):
    """Raised for any manifest format or reference problem."""


@dataclass(frozen=True)
class CurvePoint:
    time: float  # seconds
    db: float    # gain in decibels


@dataclass(frozen=True)
class TrackSpec:
    index: int
    file: str            # absolute path to the WAV file
    offset: float        # start offset on the timeline, seconds
    bus: str             # name of the destination bus
    curve: tuple = ()    # tuple[CurvePoint, ...], may be empty (unity gain)


@dataclass(frozen=True)
class BusSpec:
    name: str
    target: Optional[str]  # None -> feeds master output


@dataclass(frozen=True)
class Manifest:
    path: Path
    tracks: tuple          # tuple[TrackSpec, ...]
    buses: dict            # name -> BusSpec

    @property
    def directory(self) -> Path:
        return self.path.parent


def _fail_track(index: int, reason: str) -> None:
    raise ManifestError(f"track {index}: {reason}")


def _parse_curve(index: int, raw) -> tuple:
    if raw is None:
        return ()
    if not isinstance(raw, list):
        _fail_track(index, "curve must be a list of [time, db] pairs")
    points = []
    for k, pair in enumerate(raw):
        if (
            not isinstance(pair, (list, tuple))
            or len(pair) != 2
            or not all(isinstance(v, (int, float)) and not isinstance(v, bool) for v in pair)
        ):
            _fail_track(index, f"curve point {k} must be [time_seconds, db]")
        t, db = float(pair[0]), float(pair[1])
        if not (math.isfinite(t) and math.isfinite(db)):
            _fail_track(index, f"curve point {k} has non-finite value")
        if t < 0.0:
            _fail_track(index, f"curve point {k} has negative time {t}")
        points.append(CurvePoint(t, db))
    times = [p.time for p in points]
    if len(set(times)) != len(times):
        _fail_track(index, "curve has duplicate time values")
    if any(b <= a for a, b in zip(times, times[1:])):
        _fail_track(index, "curve times must be strictly increasing")
    return tuple(points)


def _parse_track(index: int, raw, base_dir: Path) -> TrackSpec:
    if not isinstance(raw, dict):
        _fail_track(index, "entry must be an object")
    file = raw.get("file")
    if not isinstance(file, str) or not file:
        _fail_track(index, "missing or invalid 'file'")
    path = Path(file)
    if not path.is_absolute():
        path = base_dir / path
    offset = raw.get("offset", 0.0)
    if not isinstance(offset, (int, float)) or isinstance(offset, bool):
        _fail_track(index, "offset must be a number of seconds")
    offset = float(offset)
    if not math.isfinite(offset) or offset < 0.0:
        _fail_track(index, f"offset {offset!r} must be finite and >= 0")
    bus = raw.get("bus", "master")
    if not isinstance(bus, str) or not bus:
        _fail_track(index, "bus must be a non-empty string")
    curve = _parse_curve(index, raw.get("curve"))
    return TrackSpec(index=index, file=str(path), offset=offset, bus=bus, curve=curve)


def _parse_buses(raw) -> dict:
    buses: dict[str, BusSpec] = {"master": BusSpec("master", None)}
    if raw is None:
        return buses
    if not isinstance(raw, dict):
        raise ManifestError("'buses' must be an object mapping name -> {target}")
    for name, spec in raw.items():
        if not isinstance(name, str) or not name:
            raise ManifestError("bus names must be non-empty strings")
        if not isinstance(spec, dict):
            raise ManifestError(f"bus {name!r}: spec must be an object")
        target = spec.get("target")
        if target is not None and not isinstance(target, str):
            raise ManifestError(f"bus {name!r}: target must be a string or null")
        buses[name] = BusSpec(name, target)
    return buses


def load_manifest(path) -> Manifest:
    """Load and validate a manifest JSON file."""
    mpath = Path(path)
    try:
        raw = json.loads(mpath.read_text(encoding="utf-8"))
    except FileNotFoundError:
        raise ManifestError(f"manifest not found: {mpath}")
    except json.JSONDecodeError as exc:
        raise ManifestError(f"manifest is not valid JSON: {exc}")
    if not isinstance(raw, dict):
        raise ManifestError("manifest root must be an object")
    buses = _parse_buses(raw.get("buses"))
    raw_tracks = raw.get("tracks")
    if not isinstance(raw_tracks, list) or not raw_tracks:
        raise ManifestError("'tracks' must be a non-empty list")
    if len(raw_tracks) > MAX_TRACKS:
        raise ManifestError(
            f"too many tracks: {len(raw_tracks)} > {MAX_TRACKS}"
        )
    tracks = tuple(
        _parse_track(i, entry, mpath.parent) for i, entry in enumerate(raw_tracks)
    )
    for tr in tracks:
        if tr.bus not in buses:
            _fail_track(tr.index, f"references unknown bus {tr.bus!r}")
    return Manifest(path=mpath, tracks=tracks, buses=buses)
