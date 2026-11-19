"""Primitive JSON validation helpers shared by the project model."""

from __future__ import annotations

import math
from typing import Any, Mapping

from .errors import ConfigError


def fail(location: str, message: str) -> None:
    raise ConfigError(message, location=location)


def seconds_to_frames(seconds: float, sample_rate: int) -> int:
    """Round a non-negative duration to whole frames (half away from zero)."""

    scaled = seconds * sample_rate
    if not math.isfinite(scaled):
        raise ConfigError("duration is too large", location="duration")
    return int(math.floor(scaled + 0.5))


def mapping(value: Any, location: str) -> Mapping[str, Any]:
    if not isinstance(value, dict):
        fail(location, f"expected an object, got {type(value).__name__}")
    return value


def string(obj: Mapping[str, Any], key: str, location: str, *, default: str | None = None) -> str:
    value = obj.get(key, default)
    if not isinstance(value, str) or not value.strip():
        fail(f"{location}.{key}", "expected a non-empty string")
    return value


def boolean(obj: Mapping[str, Any], key: str, location: str, *, default: bool = False) -> bool:
    value = obj.get(key, default)
    if not isinstance(value, bool):
        fail(f"{location}.{key}", "expected true or false")
    return value


def integer(obj: Mapping[str, Any], key: str, location: str, *, default: int | None = None) -> int:
    value = obj.get(key, default)
    if isinstance(value, bool) or not isinstance(value, int):
        fail(f"{location}.{key}", "expected an integer")
    return value


def number(obj: Mapping[str, Any], key: str, location: str, *, default: float | None = None) -> float:
    value = obj.get(key, default)
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        fail(f"{location}.{key}", "expected a number")
    result = float(value)
    if not math.isfinite(result):
        fail(f"{location}.{key}", "expected a finite number")
    return result


def gain_db(obj: Mapping[str, Any], key: str, location: str, *, default: float = 0.0) -> float:
    value = number(obj, key, location, default=default)
    if not math.isfinite(10.0 ** (value / 20.0)):
        fail(f"{location}.{key}", "gain_db is outside the representable range")
    return value


def frame_field(
    obj: Mapping[str, Any],
    frame_key: str,
    seconds_key: str,
    sample_rate: int,
    location: str,
    *,
    default: int | None = 0,
) -> int:
    """Read a position expressed as either an integer frame or seconds."""

    has_frame = frame_key in obj
    has_seconds = seconds_key in obj
    if has_frame and has_seconds:
        fail(location, f"declare either {frame_key} or {seconds_key}, not both")
    if has_frame:
        frame = integer(obj, frame_key, location, default=default)
        if isinstance(obj.get(frame_key), bool):
            fail(f"{location}.{frame_key}", "expected an integer")
    elif has_seconds:
        seconds = number(obj, seconds_key, location)
        if seconds < 0:
            fail(f"{location}.{seconds_key}", "must be non-negative")
        frame = seconds_to_frames(seconds, sample_rate)
    elif default is not None:
        frame = default
    else:
        fail(location, f"missing {frame_key} or {seconds_key}")
    if frame is None or frame < 0:
        fail(location, "frame position must be non-negative")
    return frame
