"""Read the cutline v1 exchange format.

v1
    Integer frames plus an explicit frame rate on every track.  Clip timing is
    ``start_frame`` / ``duration_frames``.

The document keeps a top-level ``version`` key, a ``timeline`` object, an
ordered ``tracks`` list and an ordered ``clips`` list.  The reader validates
the version, required fields and unknown fields (policy: ``error``,
``warn`` or ``ignore``); v2 and writers follow in later commits.
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Iterable

from .model import ModelError, ModelIssue, Timeline, build_timeline
from .rational import RationalTime, TimeError, format_fraction, parse_rate, parse_seconds

SUPPORTED_VERSIONS = ("v1",)
UNKNOWN_POLICIES = ("error", "warn", "ignore")

_V1_TOP = {"version", "timeline"}
_V1_TIMELINE = {"id", "name", "metadata", "tracks"}
_V1_TRACK = {"id", "name", "rate", "metadata", "clips"}
_V1_CLIP = {"id", "name", "start_frame", "duration_frames", "rate", "priority", "metadata"}



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

    data = _decode_v1(timeline_raw, checker)

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


