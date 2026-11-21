"""Input validation: JSON -> Timeline, with field-path errors.

Decimals are parsed as exact Fractions so keyframe interpolation never
accumulates floating-point drift.
"""

from __future__ import annotations

import json
from fractions import Fraction
from pathlib import Path

from .model import (
    BLEND_MODES,
    MAX_FRAMES,
    MAX_INPUT_PIXELS,
    MAX_LAYERS,
    AudioEvent,
    FrameRate,
    InputError,
    Keyframe,
    Layer,
    Timeline,
)


def _require(cond: bool, path: str, message: str) -> None:
    if not cond:
        raise InputError(path, message)


def _int(value: object, path: str, minimum: int | None = None) -> int:
    _require(isinstance(value, int) and not isinstance(value, bool), path,
             f"expected integer, got {value!r}")
    if minimum is not None:
        _require(value >= minimum, path, f"must be >= {minimum}, got {value}")
    return value


def _fraction(value: object, path: str) -> Fraction:
    _require(isinstance(value, (Fraction, int)) and not isinstance(value, bool),
             path, f"expected number, got {value!r}")
    return value if isinstance(value, Fraction) else Fraction(value)


def _parse_layer(raw: object, i: int) -> Layer:
    path = f"layers[{i}]"
    _require(isinstance(raw, dict), path, "expected object")
    name = raw.get("name")
    _require(isinstance(name, str) and name, f"{path}.name", "expected non-empty string")
    png = raw.get("png")
    _require(isinstance(png, str) and png, f"{path}.png", "expected non-empty string")
    start = _int(raw.get("start_frame"), f"{path}.start_frame", 0)
    end = _int(raw.get("end_frame"), f"{path}.end_frame", 0)
    _require(end > start, f"{path}.end_frame",
             f"must be greater than start_frame ({start}), got {end}")
    blend = raw.get("blend", "normal")
    _require(blend in BLEND_MODES, f"{path}.blend",
             f"unknown blend mode {blend!r}; expected one of {BLEND_MODES}")
    z = _int(raw.get("z", 0), f"{path}.z")
    has_opacity = "opacity" in raw
    has_keyframes = "keyframes" in raw
    _require(not (has_opacity and has_keyframes), path,
             "specify either opacity or keyframes, not both")
    opacity = Fraction(1)
    keyframes: tuple[Keyframe, ...] = ()
    if has_opacity:
        opacity = _fraction(raw["opacity"], f"{path}.opacity")
        _require(Fraction(0) <= opacity <= 1, f"{path}.opacity",
                 f"must be in [0, 1], got {float(opacity)}")
    elif has_keyframes:
        raw_kfs = raw["keyframes"]
        _require(isinstance(raw_kfs, list) and raw_kfs, f"{path}.keyframes",
                 "expected non-empty array")
        parsed = []
        for j, kf in enumerate(raw_kfs):
            kp = f"{path}.keyframes[{j}]"
            _require(isinstance(kf, dict), kp, "expected object")
            frame = _int(kf.get("frame"), f"{kp}.frame", 0)
            value = _fraction(kf.get("value"), f"{kp}.value")
            _require(Fraction(0) <= value <= 1, f"{kp}.value",
                     f"must be in [0, 1], got {float(value)}")
            parsed.append(Keyframe(frame, value))
        parsed.sort(key=lambda k: k.frame)
        for a, b in zip(parsed, parsed[1:]):
            _require(a.frame < b.frame, f"{path}.keyframes",
                     f"duplicate keyframe at frame {a.frame}")
        keyframes = tuple(parsed)
    return Layer(name, png, start, end, opacity, keyframes, blend, z, i)


def load_timeline(path: str | Path, width: int, height: int) -> Timeline:
    """Parse and fully validate a timeline JSON file."""
    path = Path(path)
    _require(width > 0 and height > 0, "canvas",
             f"width and height must be positive, got {width}x{height}")
    try:
        with open(path, encoding="utf-8") as fh:
            raw = json.load(fh, parse_float=Fraction)
    except OSError as exc:
        raise InputError("input", f"cannot read file: {exc}") from exc
    except json.JSONDecodeError as exc:
        raise InputError("input", f"invalid JSON: {exc}") from exc
    _require(isinstance(raw, dict), "input", "top level must be an object")

    rate = raw.get("frame_rate")
    _require(isinstance(rate, dict), "frame_rate", "expected object {num, den}")
    num = _int(rate.get("num"), "frame_rate.num", 1)
    den = _int(rate.get("den"), "frame_rate.den", 1)
    frame_rate = FrameRate(num, den)

    duration = raw.get("duration")
    _require(isinstance(duration, dict), "duration",
             "expected object {num, den} in seconds")
    dur = Fraction(_int(duration.get("num"), "duration.num", 1),
                   _int(duration.get("den"), "duration.den", 1))
    frames = dur * frame_rate.num / frame_rate.den
    _require(frames.denominator == 1, "duration",
             f"duration {float(dur)}s is not a whole number of frames at "
             f"{num}/{den} fps")
    frame_count = frames.numerator
    _require(1 <= frame_count <= MAX_FRAMES, "duration",
             f"frame count {frame_count} outside [1, {MAX_FRAMES}]")

    raw_layers = raw.get("layers")
    _require(isinstance(raw_layers, list), "layers", "expected array")
    _require(len(raw_layers) <= MAX_LAYERS, "layers",
             f"too many layers: {len(raw_layers)} > {MAX_LAYERS}")
    layers = tuple(_parse_layer(l, i) for i, l in enumerate(raw_layers))
    names = [l.name for l in layers]
    _require(len(set(names)) == len(names), "layers", "layer names must be unique")
    for i, layer in enumerate(layers):
        _require(layer.end_frame <= frame_count, f"layers[{i}].end_frame",
                 f"{layer.end_frame} exceeds timeline frame count {frame_count}")
    _require(width * height * max(1, len(layers)) <= MAX_INPUT_PIXELS, "layers",
             "input pixel total exceeds limit")

    audio = raw.get("audio")
    sample_rate = 0
    events: tuple[AudioEvent, ...] = ()
    if audio is not None:
        _require(isinstance(audio, dict), "audio", "expected object")
        sample_rate = _int(audio.get("sample_rate"), "audio.sample_rate", 1)
        raw_events = audio.get("events", [])
        _require(isinstance(raw_events, list), "audio.events", "expected array")
        total = Fraction(frame_count * frame_rate.den * sample_rate,
                         frame_rate.num)
        parsed_events = []
        for j, ev in enumerate(raw_events):
            ep = f"audio.events[{j}]"
            _require(isinstance(ev, dict), ep, "expected object")
            ename = ev.get("name")
            _require(isinstance(ename, str) and ename, f"{ep}.name",
                     "expected non-empty string")
            sample = _int(ev.get("sample"), f"{ep}.sample", 0)
            _require(Fraction(sample) <= total, f"{ep}.sample",
                     f"event beyond timeline end ({sample} > {float(total)} samples)")
            parsed_events.append(AudioEvent(ename, sample))
        events = tuple(parsed_events)

    return Timeline(
        frame_rate=frame_rate,
        frame_count=frame_count,
        layers=layers,
        sample_rate=sample_rate,
        events=events,
        base_dir=path.resolve().parent,
    )
