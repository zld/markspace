"""Read and write the cutline v1 and v2 exchange formats.

v1
    Integer frames plus an explicit frame rate on every track.  Clip timing is
    ``start_frame`` / ``duration_frames``.

v2
    Exact rational seconds (``"1201/1000"`` or ``"2"``), with optional track
    metadata and a per-clip rate.

Both versions keep a top-level ``version`` key, a ``timeline`` object, an
ordered ``tracks`` list and an ordered ``clips`` list.  Readers validate the
version, required fields and unknown fields (policy: ``error``, ``warn`` or
``ignore``).  Writers can emit either version with stable key order, so the
same model serialises to byte-identical files across runs.
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Iterable

from .model import ModelError, ModelIssue, Timeline, build_timeline, validate_timeline
from .rational import RationalTime, TimeError, format_fraction, parse_rate, parse_seconds

SUPPORTED_VERSIONS = ("v1", "v2")
UNKNOWN_POLICIES = ("error", "warn", "ignore")

_V1_TOP = {"version", "timeline"}
_V1_TIMELINE = {"id", "name", "metadata", "tracks"}
_V1_TRACK = {"id", "name", "rate", "metadata", "clips"}
_V1_CLIP = {"id", "name", "start_frame", "duration_frames", "rate", "priority", "metadata"}

_V2_TOP = {"version", "timeline"}
_V2_TIMELINE = {"id", "name", "metadata", "tracks"}
_V2_TRACK = {"id", "name", "rate", "metadata", "clips"}
_V2_CLIP = {"id", "name", "start", "duration", "rate", "priority", "metadata"}


class ExchangeError(ValueError):
    """Malformed exchange file, with locating details."""

    def __init__(self, path: str, message: str, details: Iterable[ModelIssue] | None = None):
        self.path = path
        self.message = message
        self.details = list(details or [])
        text = f"{path}: {message}"
        if self.details:
            text += " (" + "; ".join(f"{d.path}: {d.message}" for d in self.details) + ")"
        super().__init__(text)


@dataclass
class LoadedExchange:
    timeline: Timeline
    version: str
    warnings: list[str] = field(default_factory=list)


def load_exchange(
    path: str | Path,
    *,
    unknown_fields: str = "error",
    expected_version: str | None = None,
) -> LoadedExchange:
    """Load a timeline from ``path``."""
    if unknown_fields not in UNKNOWN_POLICIES:
        raise ExchangeError("<args>", f"unknown field policy must be one of {UNKNOWN_POLICIES}")
    file_path = Path(path)
    try:
        raw_text = file_path.read_text(encoding="utf-8")
    except OSError as exc:
        raise ExchangeError("<file>", f"cannot read {file_path}: {exc.strerror or exc}") from None
    try:
        payload = json.loads(raw_text)
    except json.JSONDecodeError as exc:
        raise ExchangeError(
            "<file>",
            f"{file_path.name}: malformed JSON at line {exc.lineno} column {exc.colno}: {exc.msg}",
        ) from None
    if not isinstance(payload, dict):
        raise ExchangeError("<root>", "exchange document must be a JSON object")

    version = payload.get("version")
    if version not in SUPPORTED_VERSIONS:
        raise ExchangeError(
            "version",
            f"unsupported exchange version {version!r}; expected one of {', '.join(SUPPORTED_VERSIONS)}",
        )
    if expected_version is not None and version != expected_version:
        raise ExchangeError("version", f"expected version {expected_version!r}, got {version!r}")

    warnings: list[str] = []
    checker = _UnknownChecker(unknown_fields, warnings)
    checker.check(payload, _V1_TOP if version == "v1" else _V2_TOP, "root")
    timeline_raw = payload.get("timeline")
    if not isinstance(timeline_raw, dict):
        raise ExchangeError("timeline", "required object 'timeline' is missing or not an object")

    if version == "v1":
        data = _decode_v1(timeline_raw, checker)
    else:
        data = _decode_v2(timeline_raw, checker)

    try:
        timeline = build_timeline(data)
    except ModelError as exc:
        raise ExchangeError("timeline", "invalid timeline data", exc.issues) from None
    return LoadedExchange(timeline=timeline, version=version, warnings=warnings)


class _UnknownChecker:
    def __init__(self, policy: str, warnings: list[str]) -> None:
        self.policy = policy
        self.warnings = warnings

    def check(self, obj: dict[str, Any], allowed: set[str], path: str) -> None:
        extras = sorted(set(obj) - allowed)
        if not extras:
            return
        message = f"unknown field(s) {', '.join(extras)} at {path}"
        if self.policy == "error":
            raise ExchangeError(path, f"unknown field(s) {', '.join(extras)}")
        if self.policy == "warn":
            self.warnings.append(message)


def _require(obj: dict[str, Any], key: str, path: str) -> Any:
    if key not in obj:
        raise ExchangeError(path, f"required field {key!r} is missing")
    return obj[key]


def _metadata(obj: dict[str, Any], path: str) -> dict[str, Any]:
    metadata = obj.get("metadata", {})
    if not isinstance(metadata, dict):
        raise ExchangeError(f"{path}.metadata", "must be an object")
    return dict(metadata)


def _decode_v1(timeline_raw: dict[str, Any], checker: _UnknownChecker) -> dict[str, Any]:
    checker.check(timeline_raw, _V1_TIMELINE, "timeline")
    tracks_raw = _require(timeline_raw, "tracks", "timeline")
    if not isinstance(tracks_raw, list):
        raise ExchangeError("timeline.tracks", "must be a list")

    tracks: list[dict[str, Any]] = []
    for t_index, track_raw in enumerate(tracks_raw):
        t_path = f"timeline.tracks[{t_index}]"
        if not isinstance(track_raw, dict):
            raise ExchangeError(t_path, "must be an object")
        checker.check(track_raw, _V1_TRACK, t_path)
        track_metadata = _metadata(track_raw, t_path)
        try:
            rate = parse_rate(_require(track_raw, "rate", t_path), f"{t_path}.rate")
        except TimeError as exc:
            raise ExchangeError(str(exc.path or t_path), str(exc)) from None
        clips_raw = track_raw.get("clips", [])
        if not isinstance(clips_raw, list):
            raise ExchangeError(f"{t_path}.clips", "must be a list")
        clips: list[dict[str, Any]] = []
        for c_index, clip_raw in enumerate(clips_raw):
            c_path = f"{t_path}.clips[{c_index}]"
            if not isinstance(clip_raw, dict):
                raise ExchangeError(c_path, "must be an object")
            checker.check(clip_raw, _V1_CLIP, c_path)
            clips.append(_decode_v1_clip(clip_raw, c_path, rate))
        tracks.append(
            {
                "id": track_raw.get("id", f"track-{t_index}"),
                "name": track_raw.get("name"),
                "rate": format_fraction(rate),
                "metadata": track_metadata,
                "clips": clips,
            }
        )
    return {
        "id": timeline_raw.get("id", "timeline"),
        "name": timeline_raw.get("name"),
        "metadata": _metadata(timeline_raw, "timeline"),
        "tracks": tracks,
    }


def _decode_v1_clip(
    clip_raw: dict[str, Any], path: str, track_rate: Any
) -> dict[str, Any]:
    start_frames = _require(clip_raw, "start_frame", path)
    duration_frames = _require(clip_raw, "duration_frames", path)
    for label, value in (("start_frame", start_frames), ("duration_frames", duration_frames)):
        if isinstance(value, bool) or not isinstance(value, int):
            raise ExchangeError(f"{path}.{label}", f"must be an integer, got {value!r}")
    try:
        rate = parse_rate(clip_raw.get("rate", track_rate), f"{path}.rate")
    except TimeError as exc:
        raise ExchangeError(str(exc.path or path), str(exc)) from None
    priority = clip_raw.get("priority", 0)
    if isinstance(priority, bool) or not isinstance(priority, int):
        raise ExchangeError(f"{path}.priority", "must be an integer")
    return {
        "id": clip_raw.get("id"),
        "name": clip_raw.get("name"),
        "start": format_fraction(RationalTime.from_frames(start_frames, rate).seconds),
        "duration": format_fraction(RationalTime.from_frames(duration_frames, rate).seconds),
        "rate": format_fraction(rate),
        "priority": priority,
        "metadata": _metadata(clip_raw, path),
    }


def _decode_v2(timeline_raw: dict[str, Any], checker: _UnknownChecker) -> dict[str, Any]:
    checker.check(timeline_raw, _V2_TIMELINE, "timeline")
    tracks_raw = _require(timeline_raw, "tracks", "timeline")
    if not isinstance(tracks_raw, list):
        raise ExchangeError("timeline.tracks", "must be a list")

    tracks: list[dict[str, Any]] = []
    for t_index, track_raw in enumerate(tracks_raw):
        t_path = f"timeline.tracks[{t_index}]"
        if not isinstance(track_raw, dict):
            raise ExchangeError(t_path, "must be an object")
        checker.check(track_raw, _V2_TRACK, t_path)
        try:
            rate = parse_rate(track_raw.get("rate", 24), f"{t_path}.rate")
        except TimeError as exc:
            raise ExchangeError(str(exc.path or t_path), str(exc)) from None
        metadata = track_raw.get("metadata", {})
        if not isinstance(metadata, dict):
            raise ExchangeError(f"{t_path}.metadata", "must be an object")
        clips_raw = track_raw.get("clips", [])
        if not isinstance(clips_raw, list):
            raise ExchangeError(f"{t_path}.clips", "must be a list")
        clips: list[dict[str, Any]] = []
        for c_index, clip_raw in enumerate(clips_raw):
            c_path = f"{t_path}.clips[{c_index}]"
            if not isinstance(clip_raw, dict):
                raise ExchangeError(c_path, "must be an object")
            checker.check(clip_raw, _V2_CLIP, c_path)
            clips.append(_decode_v2_clip(clip_raw, c_path, rate))
        tracks.append(
            {
                "id": track_raw.get("id", f"track-{t_index}"),
                "name": track_raw.get("name"),
                "rate": format_fraction(rate),
                "metadata": dict(metadata),
                "clips": clips,
            }
        )
    timeline_metadata = timeline_raw.get("metadata", {})
    if not isinstance(timeline_metadata, dict):
        raise ExchangeError("timeline.metadata", "must be an object")
    return {
        "id": timeline_raw.get("id", "timeline"),
        "name": timeline_raw.get("name"),
        "metadata": dict(timeline_metadata),
        "tracks": tracks,
    }


def _decode_v2_clip(clip_raw: dict[str, Any], path: str, track_rate: Any) -> dict[str, Any]:
    start_raw = _require(clip_raw, "start", path)
    duration_raw = _require(clip_raw, "duration", path)
    try:
        start = parse_seconds(start_raw, f"{path}.start")
        duration = parse_seconds(duration_raw, f"{path}.duration")
    except TimeError as exc:
        raise ExchangeError(str(exc.path or path), str(exc)) from None
    try:
        rate = parse_rate(clip_raw.get("rate", track_rate), f"{path}.rate")
    except TimeError as exc:
        raise ExchangeError(str(exc.path or path), str(exc)) from None
    priority = clip_raw.get("priority", 0)
    if isinstance(priority, bool) or not isinstance(priority, int):
        raise ExchangeError(f"{path}.priority", "must be an integer")
    metadata = clip_raw.get("metadata", {})
    if not isinstance(metadata, dict):
        raise ExchangeError(f"{path}.metadata", "must be an object")
    return {
        "id": clip_raw.get("id"),
        "name": clip_raw.get("name"),
        "start": format_fraction(start),
        "duration": format_fraction(duration),
        "rate": format_fraction(rate),
        "priority": priority,
        "metadata": dict(metadata),
    }


# ---------------------------------------------------------------------------
# writing
# ---------------------------------------------------------------------------


def dump_exchange(timeline: Timeline, version: str = "v2") -> dict[str, Any]:
    """Serialise ``timeline`` to a JSON-ready dict in the requested version."""
    if version not in SUPPORTED_VERSIONS:
        raise ExchangeError("version", f"unsupported exchange version {version!r}")
    validate_timeline(timeline)

    if version == "v1":
        return _downgrade_to_v1(timeline)
    return _encode_v2(timeline)


def _downgrade_to_v1(timeline: Timeline) -> dict[str, Any]:
    """Encode v1 (integer frames plus an explicit rate).

    Frames are rounded once from the exact rational and never accumulated, so
    a v1 file holds timing to within one target frame.  Optional metadata is
    carried through so a v2 -> v1 -> v2 round trip is lossless.
    """
    return {
        "version": "v1",
        "timeline": {
            "id": timeline.id,
            "name": timeline.name,
            "metadata": dict(timeline.metadata),
            "tracks": [
                {
                    "id": track.id,
                    "name": track.name,
                    "rate": format_fraction(track.rate),
                    "metadata": dict(track.metadata),
                    "clips": [
                        {
                            "id": clip.id,
                            "name": clip.name,
                            "start_frame": clip.start.to_frames(clip.rate),
                            "duration_frames": max(1, clip.duration.to_frames(clip.rate)),
                            "rate": format_fraction(clip.rate),
                            "priority": clip.priority,
                            "metadata": dict(clip.metadata),
                        }
                        for clip in track.clips
                    ],
                }
                for track in timeline.tracks
            ],
        },
    }


def _encode_v2(timeline: Timeline) -> dict[str, Any]:
    return {
        "version": "v2",
        "timeline": {
            "id": timeline.id,
            "name": timeline.name,
            "metadata": dict(timeline.metadata),
            "tracks": [
                {
                    "id": track.id,
                    "name": track.name,
                    "rate": format_fraction(track.rate),
                    "metadata": dict(track.metadata),
                    "clips": [
                        {
                            "id": clip.id,
                            "name": clip.name,
                            "start": clip.start.as_seconds_text(),
                            "duration": clip.duration.as_seconds_text(),
                            "rate": format_fraction(clip.rate),
                            "priority": clip.priority,
                            "metadata": dict(clip.metadata),
                        }
                        for clip in track.clips
                    ],
                }
                for track in timeline.tracks
            ],
        },
    }


def write_exchange(
    timeline: Timeline,
    path: str | Path,
    version: str = "v2",
    *,
    forbidden_paths: Iterable[str | Path] = (),
) -> Path:
    """Write ``timeline`` atomically, refusing to overwrite protected inputs."""
    target = Path(path)
    protected = {Path(item).resolve() for item in forbidden_paths}
    if target.resolve() in protected:
        raise ExchangeError(str(target), "refusing to overwrite an input file")
    if target.exists() and target.is_dir():
        raise ExchangeError(str(target), "output path is a directory")
    payload = dump_exchange(timeline, version)
    target.parent.mkdir(parents=True, exist_ok=True)
    tmp = target.with_name(f".{target.name}.tmp")
    tmp.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")
    tmp.replace(target)
    return target
