"""Timeline schema parsing and validation.

A timeline is a plain mapping (as loaded from JSON) describing an exact frame
rate, a canvas, an ordered set of layers with independent keyframe tracks,
and integer audio cues. Parsing produces immutable dataclasses; anything
malformed, unsupported, or out of range raises :class:`SchemaError` before
any pixels are touched.

Layer stacking uses an explicit ``order`` integer; ties are broken by the
order of declaration, so the result is always deterministic.
"""

from __future__ import annotations

import posixpath
import re
from dataclasses import dataclass
from fractions import Fraction
from typing import Any, Iterable, Mapping

from .clock import ClockError, FrameClock

MAX_CANVAS_PIXELS = 16_000_000
MAX_FRAMES = 100_000
MAX_CANVAS_DIMENSION = 65_535
BLEND_MODES = frozenset({"normal", "multiply", "screen"})

_RATIONAL_RE = re.compile(r"^\s*[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:/[+-]?\d+)?\s*$")


class SchemaError(ValueError):
    """Raised when a timeline document is malformed or unsupported."""


@dataclass(frozen=True)
class Canvas:
    width: int
    height: int

    @property
    def pixels(self) -> int:
        return self.width * self.height


@dataclass(frozen=True)
class Keyframe:
    """One keyframe: a time in seconds and a value (scalar or x/y pair)."""

    time: Fraction
    value: Fraction | tuple[Fraction, Fraction]


@dataclass(frozen=True)
class Track:
    """A sorted, non-empty keyframe track with clamped linear interpolation."""

    keyframes: tuple[Keyframe, ...]

    def evaluate(self, timestamp: Fraction) -> Fraction | tuple[Fraction, Fraction]:
        """Linearly interpolate at ``timestamp``; clamp outside the range."""
        frames = self.keyframes
        if timestamp <= frames[0].time:
            return frames[0].value
        if timestamp >= frames[-1].time:
            return frames[-1].value
        # Small key counts: a linear scan keeps exact rational arithmetic.
        for previous, current in zip(frames, frames[1:]):
            if timestamp <= current.time:
                span = current.time - previous.time
                if span == 0:
                    return current.value
                ratio = (timestamp - previous.time) / span
                if isinstance(previous.value, tuple):
                    x0, y0 = previous.value
                    x1, y1 = current.value  # type: ignore[misc]
                    return (x0 + (x1 - x0) * ratio, y0 + (y1 - y0) * ratio)
                return previous.value + (current.value - previous.value) * ratio  # type: ignore[operator]
        return frames[-1].value


@dataclass(frozen=True)
class Layer:
    id: str
    source: str
    order: int
    declaration_index: int
    start: Fraction
    end: Fraction
    blend: str
    position: Track
    scale: Track
    opacity: Track

    def is_active(self, timestamp: Fraction) -> bool:
        """Half-open activity: ``start <= t < end``."""
        return self.start <= timestamp < self.end


@dataclass(frozen=True)
class AudioEvent:
    label: str
    sample: int
    sample_rate: int

    @property
    def timestamp(self) -> Fraction:
        return Fraction(self.sample, self.sample_rate)


@dataclass(frozen=True)
class Timeline:
    fps: Fraction
    duration: Fraction
    canvas: Canvas
    layers: tuple[Layer, ...]
    audio_events: tuple[AudioEvent, ...]

    @property
    def clock(self) -> FrameClock:
        return FrameClock.from_rate(self.fps, self.duration)

    @property
    def frame_count(self) -> int:
        return self.clock.frame_count

    def draw_order(self) -> tuple[Layer, ...]:
        """Layers bottom-to-top, stable on declaration order for equal ranks."""
        return tuple(sorted(self.layers, key=lambda layer: (layer.order, layer.declaration_index)))


def parse_timeline(document: Mapping[str, Any]) -> Timeline:
    """Validate ``document`` and return an immutable :class:`Timeline`."""
    if not isinstance(document, Mapping):
        raise SchemaError("timeline must be a JSON object")
    width, height = _parse_canvas(_require(document, "canvas", Mapping))
    canvas = Canvas(width, height)

    fps = _parse_rational(_require(document, "fps", (str, int)), "fps")
    if fps <= 0:
        raise SchemaError(f"fps must be positive, got {fps}")

    if "frames" in document and "duration_seconds" in document:
        raise SchemaError("specify either 'frames' or 'duration_seconds', not both")
    if "frames" in document:
        frames = _parse_int(document["frames"], "frames")
        if frames < 0:
            raise SchemaError("frames must be non-negative")
        if frames > MAX_FRAMES:
            raise SchemaError(f"frames {frames} exceeds limit {MAX_FRAMES}")
        duration = Fraction(frames, 1) / fps
    elif "duration_seconds" in document:
        duration = _parse_rational(document["duration_seconds"], "duration_seconds")
        if duration < 0:
            raise SchemaError("duration_seconds must be non-negative")
    else:
        raise SchemaError("timeline requires 'frames' or 'duration_seconds'")

    clock = FrameClock.from_rate(fps, duration)
    if clock.frame_count > MAX_FRAMES:
        raise SchemaError(f"computed frame count {clock.frame_count} exceeds {MAX_FRAMES}")

    layers = _parse_layers(document.get("layers", []), duration)
    audio_events = _parse_audio_events(document.get("audio_events", []))
    return Timeline(
        fps=fps,
        duration=duration,
        canvas=canvas,
        layers=layers,
        audio_events=audio_events,
    )


def _parse_canvas(raw: Any) -> tuple[int, int]:
    if not isinstance(raw, Mapping):
        raise SchemaError("canvas must be an object with width and height")
    width = _parse_int(_require(raw, "width", int), "canvas.width")
    height = _parse_int(_require(raw, "height", int), "canvas.height")
    if width <= 0 or height <= 0:
        raise SchemaError(f"canvas dimensions must be positive, got {width}x{height}")
    if width > MAX_CANVAS_DIMENSION or height > MAX_CANVAS_DIMENSION:
        raise SchemaError(f"canvas dimension exceeds {MAX_CANVAS_DIMENSION}")
    if width * height > MAX_CANVAS_PIXELS:
        raise SchemaError(
            f"canvas {width}x{height} exceeds {MAX_CANVAS_PIXELS} pixel limit"
        )
    return width, height


def _parse_layers(raw: Any, duration: Fraction) -> tuple[Layer, ...]:
    if raw is None:
        raw = []
    if isinstance(raw, (str, bytes)) or not isinstance(raw, Iterable):
        raise SchemaError("layers must be an array")
    seen: set[str] = set()
    layers: list[Layer] = []
    for index, item in enumerate(raw):
        if not isinstance(item, Mapping):
            raise SchemaError(f"layers[{index}] must be an object")
        layer_id = _require(item, "id", str)
        if not layer_id.strip():
            raise SchemaError(f"layers[{index}].id must be non-empty")
        if layer_id in seen:
            raise SchemaError(f"duplicate layer id {layer_id!r}")
        seen.add(layer_id)
        source = _parse_source(_require(item, "source", str), layer_id)
        order = _parse_int(item.get("order", 0), f"layers[{index}].order")
        start = _parse_time(item.get("start", 0), f"layers[{index}].start")
        end = _parse_time(item.get("end", duration), f"layers[{index}].end")
        if start < 0:
            raise SchemaError(f"layer {layer_id!r} start must be non-negative")
        if end < start:
            raise SchemaError(f"layer {layer_id!r} end precedes start")
        blend = item.get("blend", "normal")
        if blend not in BLEND_MODES:
            raise SchemaError(f"layer {layer_id!r} has unknown blend mode {blend!r}")
        layers.append(
            Layer(
                id=layer_id,
                source=source,
                order=order,
                declaration_index=index,
                start=start,
                end=end,
                blend=blend,
                position=_parse_track(
                    item.get("position"),
                    (Fraction(0), Fraction(0)),
                    f"layers[{index}].position",
                    vector=True,
                ),
                scale=_parse_track(item.get("scale"), Fraction(1), f"layers[{index}].scale"),
                opacity=_parse_track(item.get("opacity"), Fraction(1), f"layers[{index}].opacity"),
            )
        )
    return tuple(layers)


def _parse_source(source: str, layer_id: str) -> str:
    if not source or "\x00" in source or "\\" in source:
        raise SchemaError(f"layer {layer_id!r} has an invalid source path")
    if source.startswith("/") or posixpath.isabs(source):
        raise SchemaError(f"layer {layer_id!r} source must be relative to the asset root")
    parts = [part for part in source.split("/") if part not in ("", ".")]
    if any(part == ".." for part in parts):
        raise SchemaError(f"layer {layer_id!r} source escapes the asset root")
    if not parts:
        raise SchemaError(f"layer {layer_id!r} has an empty source path")
    if not parts[-1].lower().endswith(".png"):
        raise SchemaError(f"layer {layer_id!r} source must be a .png file")
    return "/".join(parts)


def _parse_track(
    raw: Any,
    default: Fraction | tuple[Fraction, Fraction],
    name: str,
    vector: bool = False,
) -> Track:
    if raw is None:
        return Track((Keyframe(Fraction(0), default),))
    if isinstance(raw, (int, str, Fraction)) and not isinstance(raw, bool):
        if vector:
            raise SchemaError(f"{name} requires both x and y")
        return Track((Keyframe(Fraction(0), _parse_rational(raw, name)),))
    if isinstance(raw, Mapping):
        if "keyframes" not in raw:
            value = _parse_value(raw, name, vector if vector else None)
            return Track((Keyframe(Fraction(0), value),))
        raw = raw["keyframes"]
    if isinstance(raw, (str, bytes)) or not isinstance(raw, Iterable):
        raise SchemaError(f"{name} must be an object or keyframe array")
    keyframes: list[Keyframe] = []
    for index, item in enumerate(raw):
        if not isinstance(item, Mapping):
            raise SchemaError(f"{name}.keyframes[{index}] must be an object")
        time = _parse_time(item.get("time"), f"{name}.keyframes[{index}].time")
        if time < 0:
            raise SchemaError(f"{name}.keyframes[{index}].time must be non-negative")
        if keyframes and time <= keyframes[-1].time:
            raise SchemaError(f"{name} keyframe times must be strictly increasing")
        value = _parse_value(item, name, True if vector else False)
        keyframes.append(Keyframe(time, value))
    if not keyframes:
        raise SchemaError(f"{name} must contain at least one keyframe")
    return Track(tuple(keyframes))


def _parse_value(
    item: Mapping[str, Any], name: str, vector: bool | None = None
) -> Fraction | tuple[Fraction, Fraction]:
    if "x" in item or "y" in item:
        if "x" not in item or "y" not in item:
            raise SchemaError(f"{name} requires both x and y")
        if vector is False:
            raise SchemaError(f"{name} is a scalar track; use 'value' instead of x/y")
        return (
            _parse_rational(item["x"], f"{name}.x"),
            _parse_rational(item["y"], f"{name}.y"),
        )
    if "value" in item:
        if vector is True:
            raise SchemaError(f"{name} is a 2-D track; use x and y instead of 'value'")
        return _parse_rational(item["value"], f"{name}.value")
    expected = "both 'x' and 'y'" if vector else "'value' (or both 'x' and 'y')"
    raise SchemaError(f"{name} needs {expected}")


def _parse_audio_events(raw: Any) -> tuple[AudioEvent, ...]:
    if raw is None:
        raw = []
    if isinstance(raw, (str, bytes)) or not isinstance(raw, Iterable):
        raise SchemaError("audio_events must be an array")
    events: list[AudioEvent] = []
    for index, item in enumerate(raw):
        if not isinstance(item, Mapping):
            raise SchemaError(f"audio_events[{index}] must be an object")
        sample = _parse_int(_require(item, "sample", int), f"audio_events[{index}].sample")
        sample_rate = _parse_int(
            _require(item, "sample_rate", int), f"audio_events[{index}].sample_rate"
        )
        if sample < 0:
            raise SchemaError(f"audio_events[{index}].sample must be non-negative")
        if sample_rate <= 0:
            raise SchemaError(f"audio_events[{index}].sample_rate must be positive")
        label = item.get("label", f"event-{index}")
        if not isinstance(label, str):
            raise SchemaError(f"audio_events[{index}].label must be a string")
        events.append(AudioEvent(label=label, sample=sample, sample_rate=sample_rate))
    return tuple(events)


def _parse_time(raw: Any, name: str) -> Fraction:
    value = _parse_rational(raw, name)
    return value


def _parse_rational(raw: Any, name: str) -> Fraction:
    if isinstance(raw, bool):
        raise SchemaError(f"{name} must be a rational number, got bool")
    if isinstance(raw, int):
        return Fraction(raw, 1)
    if isinstance(raw, Fraction):
        return raw
    if isinstance(raw, str):
        if not _RATIONAL_RE.match(raw):
            raise SchemaError(f"{name} is not a valid rational: {raw!r}")
        try:
            return Fraction(raw.strip())
        except (ValueError, ZeroDivisionError) as exc:
            raise SchemaError(f"{name} is not a valid rational: {raw!r}") from exc
    raise SchemaError(f"{name} must be a number or rational string")


def _parse_int(raw: Any, name: str) -> int:
    if isinstance(raw, bool) or not isinstance(raw, int):
        raise SchemaError(f"{name} must be an integer")
    return raw


def _require(mapping: Mapping[str, Any], key: str, expected: Any) -> Any:
    if key not in mapping:
        raise SchemaError(f"missing required field {key!r}")
    value = mapping[key]
    if expected is not None and not isinstance(value, expected):
        raise SchemaError(f"field {key!r} has wrong type {type(value).__name__}")
    return value


__all__ = [
    "AudioEvent",
    "BLEND_MODES",
    "Canvas",
    "Keyframe",
    "Layer",
    "MAX_CANVAS_PIXELS",
    "MAX_FRAMES",
    "SchemaError",
    "Timeline",
    "Track",
    "parse_timeline",
]
