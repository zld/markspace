"""Strict v1/v2 JSON exchange formats with atomic, non-destructive writes.

v1
    Integer frame coordinates, positive integer frame rate and one constant
    integer speed per clip.

v2
    Rational frame rate (``numerator``/``denominator``) and a contiguous
    ``rate_segments`` list with rational ``speed`` objects.

Unknown versions, missing fields, wrong types, duplicate ids and any field
outside the declared capability raise :class:`ExchangeError`; nothing is
silently dropped.  Writes go to a temporary file in the target directory and
are atomically renamed, and input files are never overwritten by the reader.
"""

from __future__ import annotations

import json
import os
import tempfile
from fractions import Fraction
from pathlib import Path
from typing import Any

from .errors import ExchangeError, ModelError, TimebaseError
from .model import Clip, RateSegment, Sequence, Track
from .timebase import FrameRate, require_int

SUPPORTED_VERSIONS = (1, 2)

# Defaults applied when a v1 document is read or upgraded to v2.
UPGRADE_DEFAULTS: dict[str, Any] = {
    "name": "sequence",
    "kind": "video",
    "level": 0,
    "fade_in": 0,
    "fade_out": 0,
    "priority": 0,
}

_SEQUENCE = {"id", "name", "frame_rate", "tracks"}
_TRACK = {"id", "kind", "level", "clips"}
_CLIP_COMMON = {"id", "media_id", "timeline_start", "duration", "source_in",
                "source_duration", "fade_in", "fade_out", "priority"}
_CLIP = {1: _CLIP_COMMON | {"speed"}, 2: _CLIP_COMMON | {"rate_segments"}}
_REQUIRED_CLIP = {
    1: {"id", "media_id", "timeline_start", "duration", "source_in",
        "source_duration", "speed"},
    2: {"id", "media_id", "timeline_start", "duration", "source_in",
        "source_duration", "rate_segments"},
}
_SPEED = {"numerator", "denominator"}


# --------------------------------------------------------------------------
# Validation helpers


def _fail(message: str) -> ExchangeError:
    return ExchangeError(message)


def _obj(value: Any, path: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise _fail(f"{path} must be a JSON object, got {type(value).__name__}")
    return value


def _fields(obj: dict[str, Any], allowed: set[str], required: set[str], path: str) -> None:
    unknown = sorted(set(obj) - allowed)
    if unknown:
        raise _fail(f"{path} contains unsupported field(s): {', '.join(unknown)}")
    missing = sorted(required - set(obj))
    if missing:
        raise _fail(f"{path} is missing required field(s): {', '.join(missing)}")


def _int(value: Any, path: str, minimum: int | None = None) -> int:
    return require_int(value, path, minimum=minimum)


def _guard(path: str, build: Any) -> Any:
    """Run a model constructor, translating model/timebase errors with location."""

    try:
        return build()
    except (ModelError, TimebaseError) as exc:
        detail = exc.render() if isinstance(exc, ModelError) else exc.message
        raise _fail(f"{path}: {detail}") from exc


def detect_version(document: Any) -> int:
    doc = _obj(document, "document")
    _fields(doc, {"version", "sequence"}, {"version", "sequence"}, "document")
    version = doc["version"]
    if isinstance(version, bool) or not isinstance(version, int):
        raise _fail("document.version must be an integer")
    if version not in SUPPORTED_VERSIONS:
        raise _fail(f"unsupported exchange version {version}; supported: 1, 2")
    return version


# --------------------------------------------------------------------------
# Reading


def _parse_speed(value: Any, path: str) -> Fraction:
    obj = _obj(value, path)
    _fields(obj, _SPEED, _SPEED, path)
    numerator = _int(obj["numerator"], f"{path}.numerator")
    denominator = _int(obj["denominator"], f"{path}.denominator")
    if numerator <= 0 or denominator <= 0:
        raise _fail(f"{path} must be positive, got {numerator}/{denominator}")
    return Fraction(numerator, denominator)


def _segments_for(clip_obj: dict[str, Any], path: str, version: int) -> tuple[RateSegment, ...]:
    if version == 1:
        # v1 requires the constant speed; there is no implied default.
        speed = _int(clip_obj["speed"], f"{path}.speed", minimum=1)
        return (RateSegment(0, clip_obj["duration"], Fraction(speed)),)
    items = clip_obj["rate_segments"]
    if not isinstance(items, list) or not items:
        raise _fail(f"{path}.rate_segments must be a non-empty array")
    segments = []
    for index, item in enumerate(items):
        seg_path = f"{path}.rate_segments[{index}]"
        seg = _obj(item, seg_path)
        _fields(seg, {"offset", "duration", "speed"}, {"offset", "duration", "speed"}, seg_path)
        segments.append(
            RateSegment(
                offset=_int(seg["offset"], f"{seg_path}.offset", minimum=0),
                duration=_int(seg["duration"], f"{seg_path}.duration", minimum=1),
                speed=_parse_speed(seg["speed"], f"{seg_path}.speed"),
            )
        )
    return tuple(segments)


def _parse_clip(clip_obj: Any, path: str, version: int) -> Clip:
    obj = _obj(clip_obj, path)
    _fields(obj, _CLIP[version], _REQUIRED_CLIP[version], path)
    return _guard(path, lambda: Clip(
        id=obj["id"],
        media_id=obj["media_id"],
        timeline_start=_int(obj["timeline_start"], f"{path}.timeline_start", minimum=0),
        duration=_int(obj["duration"], f"{path}.duration", minimum=1),
        source_in=_int(obj["source_in"], f"{path}.source_in", minimum=0),
        source_duration=_int(obj["source_duration"], f"{path}.source_duration", minimum=1),
        rate_segments=_segments_for(obj, path, version),
        fade_in=_int(obj.get("fade_in", UPGRADE_DEFAULTS["fade_in"]), f"{path}.fade_in", minimum=0),
        fade_out=_int(obj.get("fade_out", UPGRADE_DEFAULTS["fade_out"]), f"{path}.fade_out", minimum=0),
        priority=_int(obj.get("priority", UPGRADE_DEFAULTS["priority"]), f"{path}.priority"),
    ))


def _parse_track(track_obj: Any, path: str, version: int) -> Track:
    obj = _obj(track_obj, path)
    _fields(obj, _TRACK, {"id", "clips"}, path)
    kind = obj.get("kind", UPGRADE_DEFAULTS["kind"])
    if not isinstance(kind, str):
        raise _fail(f"{path}.kind must be a string")
    level = _int(obj.get("level", UPGRADE_DEFAULTS["level"]), f"{path}.level", minimum=0)
    if not isinstance(obj["clips"], list):
        raise _fail(f"{path}.clips must be an array")
    clips = tuple(
        _parse_clip(clip, f"{path}.clips[{index}]", version)
        for index, clip in enumerate(obj["clips"])
    )
    return _guard(path, lambda: Track(id=obj["id"], kind=kind, level=level, clips=clips))


def _parse_sequence(doc: dict[str, Any], version: int) -> Sequence:
    seq = _obj(doc["sequence"], "document.sequence")
    path = "document.sequence"
    _fields(seq, _SEQUENCE, {"id", "frame_rate", "tracks"}, path)
    name = seq.get("name", UPGRADE_DEFAULTS["name"])
    if not isinstance(name, str):
        raise _fail(f"{path}.name must be a string")
    if version == 1:
        rate = FrameRate(_int(seq["frame_rate"], f"{path}.frame_rate", minimum=1), 1)
    else:
        rate_obj = _obj(seq["frame_rate"], f"{path}.frame_rate")
        _fields(rate_obj, _SPEED, _SPEED, f"{path}.frame_rate")
        rate = _guard(f"{path}.frame_rate", lambda: FrameRate(
            _int(rate_obj["numerator"], f"{path}.frame_rate.numerator"),
            _int(rate_obj["denominator"], f"{path}.frame_rate.denominator"),
        ))
    if not isinstance(seq["tracks"], list):
        raise _fail(f"{path}.tracks must be an array")
    tracks = tuple(
        _parse_track(track, f"{path}.tracks[{index}]", version)
        for index, track in enumerate(seq["tracks"])
    )
    return _guard(path, lambda: Sequence(id=seq["id"], frame_rate=rate, tracks=tracks, name=name))


def from_document(document: Any) -> Sequence:
    try:
        version = detect_version(document)
        return _parse_sequence(document, version)
    except TimebaseError as exc:
        raise _fail(exc.message) from exc


def loads(text: str) -> Sequence:
    try:
        document = json.loads(text)
    except json.JSONDecodeError as exc:
        raise _fail(f"invalid JSON: {exc.msg} at line {exc.lineno} column {exc.colno}") from exc
    return from_document(document)


def load(path: str | os.PathLike[str]) -> Sequence:
    source = Path(path)
    try:
        text = source.read_text(encoding="utf-8")
    except OSError as exc:
        raise _fail(f"cannot read {source}: {exc.strerror or exc}") from exc
    try:
        return loads(text)
    except ExchangeError as exc:
        raise _fail(f"{source}: {exc.message}") from exc


# --------------------------------------------------------------------------
# Writing


def _clip_doc(clip: Clip, path: str, version: int) -> dict[str, Any]:
    common = {
        "id": clip.id,
        "media_id": clip.media_id,
        "timeline_start": clip.timeline_start,
        "duration": clip.duration,
        "source_in": clip.source_in,
        "source_duration": clip.source_duration,
        "fade_in": clip.fade_in,
        "fade_out": clip.fade_out,
        "priority": clip.priority,
    }
    if version == 2:
        return {**common, "rate_segments": [segment.to_json() for segment in clip.rate_segments]}
    if not clip.is_constant_speed:
        raise _fail(f"{path}: v1 cannot express a segmented rate mapping")
    speed = clip.rate_segments[0].speed
    if speed.denominator != 1:
        raise _fail(f"{path}: v1 cannot express fractional speed {speed}")
    return {**common, "speed": speed.numerator}


def to_document(sequence: Sequence, version: int = 2) -> dict[str, Any]:
    if version not in SUPPORTED_VERSIONS:
        raise _fail(f"unsupported exchange version {version}")
    if version == 1 and sequence.frame_rate.denominator != 1:
        raise _fail(f"v1 cannot express frame rate {sequence.frame_rate}; use version 2")
    rate: Any = (sequence.frame_rate.numerator if version == 1
                 else sequence.frame_rate.to_json())
    return {
        "version": version,
        "sequence": {
            "id": sequence.id,
            "name": sequence.name,
            "frame_rate": rate,
            "tracks": [
                {
                    "id": track.id,
                    "kind": track.kind,
                    "level": track.level,
                    "clips": [
                        _clip_doc(clip, f"track {track.id!r} clip {clip.id!r}", version)
                        for clip in track.clips
                    ],
                }
                for track in sequence.tracks
            ],
        },
    }


def dumps(sequence: Sequence, version: int = 2) -> str:
    return json.dumps(to_document(sequence, version), indent=2, ensure_ascii=False) + "\n"


def upgrade_v1_to_v2(document: Any) -> dict[str, Any]:
    """Validate a v1 document and return its v2 equivalent."""

    if detect_version(document) != 1:
        raise _fail("upgrade_v1_to_v2 expects a version 1 document")
    return to_document(from_document(document), 2)


def write(sequence: Sequence, path: str | os.PathLike[str], version: int = 2) -> Path:
    """Write atomically: temp file in the target directory, then ``os.replace``."""

    target = Path(path)
    payload = dumps(sequence, version).encode("utf-8")
    target.parent.mkdir(parents=True, exist_ok=True)
    fd, temp_name = tempfile.mkstemp(prefix=f".{target.name}.", suffix=".tmp", dir=target.parent)
    try:
        with os.fdopen(fd, "wb") as handle:
            handle.write(payload)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temp_name, target)
    except BaseException:
        try:
            os.unlink(temp_name)
        except FileNotFoundError:
            pass
        raise
    return target
