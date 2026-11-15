"""Track manifest loading and validation.

A manifest is a JSON object::

    {
      "target_rate": 48000,            # optional, CLI --target-rate wins
      "tracks": [
        {
          "name": "sfx",
          "path": "sfx.wav",           # relative to the manifest file
          "start_frame": 0,            # output-frame offset, >= 0
          "gain_db": -1.5,             # static trim
          "pan": 0.0,                  # -1.0 (left) .. 1.0 (right)
          "interp": "linear",          # hold | linear | cubic
          "automation": [{"frame": 0, "db": -6.0}, ...]
        }
      ]
    }

All validation failures raise :class:`ManifestError` (exit code 2) with
the offending track name in the message.
"""

from __future__ import annotations

import json
import math
from dataclasses import dataclass
from pathlib import Path

from .errors import ManifestError

MAX_START_FRAME = 2**31 - 1
INTERP_MODES = ("hold", "linear", "cubic")
DEFAULT_TARGET_RATE = 48000


@dataclass(frozen=True)
class AutomationPoint:
    frame: int
    db: float


@dataclass(frozen=True)
class TrackSpec:
    name: str
    path: Path
    start_frame: int = 0
    gain_db: float = 0.0
    pan: float = 0.0
    interp: str = "linear"
    automation: tuple[AutomationPoint, ...] = ()


@dataclass(frozen=True)
class Project:
    tracks: tuple[TrackSpec, ...]
    target_rate: int


def _fail(track: str | None, message: str) -> ManifestError:
    where = f"track {track!r}: " if track else ""
    return ManifestError(where + message)


def _parse_automation(name: str, raw: object) -> tuple[AutomationPoint, ...]:
    if raw is None:
        return ()
    if not isinstance(raw, list):
        raise _fail(name, "automation must be a list of {frame, db} points")
    points: list[AutomationPoint] = []
    prev = -1
    for i, item in enumerate(raw):
        if not isinstance(item, dict):
            raise _fail(name, f"automation point {i} must be an object")
        frame, db = item.get("frame"), item.get("db")
        if not isinstance(frame, int) or isinstance(frame, bool) or frame < 0:
            raise _fail(name, f"automation point {i}: frame must be an int >= 0")
        if not isinstance(db, (int, float)) or not math.isfinite(db):
            raise _fail(name, f"automation point {i}: db must be finite")
        if frame <= prev and points:
            raise _fail(name, "automation frames must be strictly increasing")
        prev = frame
        points.append(AutomationPoint(frame=frame, db=float(db)))
    return tuple(points)


def _parse_track(raw: object, base: Path, seen: set[str]) -> TrackSpec:
    if not isinstance(raw, dict):
        raise _fail(None, "each track must be an object")
    name = raw.get("name")
    if not isinstance(name, str) or not name:
        raise _fail(None, "track name must be a non-empty string")
    if name in seen:
        raise _fail(name, "duplicate track name")
    seen.add(name)

    path = raw.get("path")
    if not isinstance(path, str) or not path:
        raise _fail(name, "path must be a non-empty string")

    start = raw.get("start_frame", 0)
    if not isinstance(start, int) or isinstance(start, bool) or not 0 <= start <= MAX_START_FRAME:
        raise _fail(name, f"start_frame must be an int in [0, {MAX_START_FRAME}]")

    gain = raw.get("gain_db", 0.0)
    if not isinstance(gain, (int, float)) or not math.isfinite(gain):
        raise _fail(name, "gain_db must be finite")

    pan = raw.get("pan", 0.0)
    if not isinstance(pan, (int, float)) or not math.isfinite(pan) or not -1.0 <= pan <= 1.0:
        raise _fail(name, "pan must be in [-1.0, 1.0]")

    interp = raw.get("interp", "linear")
    if interp not in INTERP_MODES:
        raise _fail(name, f"interp must be one of {INTERP_MODES}")

    return TrackSpec(
        name=name,
        path=(base / path).resolve(),
        start_frame=start,
        gain_db=float(gain),
        pan=float(pan),
        interp=interp,
        automation=_parse_automation(name, raw.get("automation")),
    )


def load_manifest(path: str | Path) -> Project:
    """Parse and validate a manifest file into a :class:`Project`."""
    path = Path(path)
    try:
        raw = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise ManifestError(f"manifest not found: {path}") from exc
    except json.JSONDecodeError as exc:
        raise ManifestError(f"manifest {path}: invalid JSON: {exc}") from exc
    if not isinstance(raw, dict):
        raise ManifestError(f"manifest {path}: top level must be an object")

    rate = raw.get("target_rate", DEFAULT_TARGET_RATE)
    if not isinstance(rate, int) or isinstance(rate, bool) or rate <= 0:
        raise ManifestError(f"manifest {path}: target_rate must be a positive int")

    tracks_raw = raw.get("tracks")
    if not isinstance(tracks_raw, list) or not tracks_raw:
        raise ManifestError(f"manifest {path}: empty project, 'tracks' must be a non-empty list")

    seen: set[str] = set()
    tracks = tuple(_parse_track(t, path.parent, seen) for t in tracks_raw)
    return Project(tracks=tracks, target_rate=rate)
