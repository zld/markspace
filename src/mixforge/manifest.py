"""Manifest loading and validation.

A manifest is a JSON document describing the tracks of a mix::

    {
      "tracks": [
        {
          "id": "music",
          "path": "music.wav",
          "start": 0.25,
          "gain": [{"time": 0.0, "db": -3.0}, ...]
        },
        ...
      ]
    }

Audio paths are resolved relative to the manifest directory; paths that
escape that directory are rejected. All numeric values must be finite.
"""

from __future__ import annotations

import json
import math
from pathlib import Path

from .model import Keyframe, ManifestError, Project, Track


def _fail(message: str) -> None:
    raise ManifestError(message)


def _require_finite_number(value: object, what: str) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        _fail(f"{what} must be a number, got {value!r}")
    result = float(value)
    if not math.isfinite(result):
        _fail(f"{what} must be finite, got {value!r}")
    return result


def _parse_keyframes(raw: object, track_id: str) -> tuple[Keyframe, ...]:
    if raw is None:
        return ()
    if not isinstance(raw, list):
        _fail(f"track {track_id!r}: 'gain' must be a list")
    keyframes: list[Keyframe] = []
    for index, item in enumerate(raw):
        what = f"track {track_id!r} gain[{index}]"
        if not isinstance(item, dict):
            _fail(f"{what} must be an object")
        if "time" not in item or "db" not in item:
            _fail(f"{what} requires 'time' and 'db'")
        time = _require_finite_number(item["time"], f"{what}.time")
        db = _require_finite_number(item["db"], f"{what}.db")
        if time < 0.0:
            _fail(f"{what}.time must be >= 0, got {time}")
        keyframes.append(Keyframe(time=time, db=db))
    keyframes.sort(key=lambda k: k.time)
    for prev, cur in zip(keyframes, keyframes[1:]):
        if cur.time == prev.time:
            _fail(f"track {track_id!r}: duplicate gain keyframe time {cur.time}")
    return tuple(keyframes)


def _resolve_audio_path(manifest_dir: Path, raw_path: object, track_id: str) -> Path:
    if not isinstance(raw_path, str) or not raw_path:
        _fail(f"track {track_id!r}: 'path' must be a non-empty string")
    candidate = (manifest_dir / raw_path).resolve()
    root = manifest_dir.resolve()
    if candidate != root and root not in candidate.parents:
        _fail(f"track {track_id!r}: path {raw_path!r} escapes the manifest directory")
    if not candidate.is_file():
        _fail(f"track {track_id!r}: audio file not found: {raw_path!r}")
    return candidate


def _parse_track(raw: object, index: int, manifest_dir: Path) -> Track:
    if not isinstance(raw, dict):
        _fail(f"tracks[{index}] must be an object")
    track_id = raw.get("id")
    if not isinstance(track_id, str) or not track_id:
        _fail(f"tracks[{index}]: 'id' must be a non-empty string")
    path = _resolve_audio_path(manifest_dir, raw.get("path"), track_id)
    start = _require_finite_number(raw.get("start", 0.0), f"track {track_id!r} start")
    if start < 0.0:
        _fail(f"track {track_id!r}: start must be >= 0, got {start}")
    keyframes = _parse_keyframes(raw.get("gain"), track_id)
    return Track(track_id=track_id, path=path, start=start, keyframes=keyframes)


def load_manifest(manifest_path: str | Path) -> Project:
    """Load and validate a manifest, returning a ready-to-render Project."""
    path = Path(manifest_path)
    try:
        text = path.read_text(encoding="utf-8")
    except OSError as exc:
        raise ManifestError(f"cannot read manifest {path}: {exc}") from exc
    try:
        document = json.loads(text)
    except json.JSONDecodeError as exc:
        raise ManifestError(f"manifest {path} is not valid JSON: {exc}") from exc
    if not isinstance(document, dict):
        _fail("manifest root must be an object")
    raw_tracks = document.get("tracks")
    if not isinstance(raw_tracks, list) or not raw_tracks:
        _fail("manifest 'tracks' must be a non-empty list")
    manifest_dir = path.parent
    tracks = tuple(
        _parse_track(raw, index, manifest_dir) for index, raw in enumerate(raw_tracks)
    )
    seen: set[str] = set()
    for track in tracks:
        if track.track_id in seen:
            _fail(f"duplicate track id {track.track_id!r}")
        seen.add(track.track_id)
    return Project(tracks=tracks)
