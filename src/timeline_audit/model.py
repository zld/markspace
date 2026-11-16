"""Timeline JSON validation.

parse_timeline() turns a decoded JSON dict into a checked Timeline.
Every violation raises TimelineError with a location path such as
"layers[2].opacity[1]" so the offending entry can be found quickly.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from fractions import Fraction

from .curves import INTERP_MODES, Keyframe
from .timebase import frame_count, parse_fps, to_seconds

BLEND_MODES = ("normal", "multiply", "screen", "add")


class TimelineError(ValueError):
    """Raised when a timeline document is invalid."""


@dataclass(frozen=True)
class Layer:
    id: str
    asset: str
    start: Fraction
    end: Fraction
    x: float
    y: float
    scale: float
    blend: str
    opacity: tuple[Keyframe, ...]


@dataclass(frozen=True)
class Beep:
    time: Fraction
    freq: float
    duration: Fraction


@dataclass(frozen=True)
class Timeline:
    fps: Fraction
    width: int
    height: int
    duration: Fraction
    frames: int
    layers: tuple[Layer, ...] = field(default=())
    sample_rate: int = 48000
    beeps: tuple[Beep, ...] = field(default=())


def _err(where: str, msg: str) -> TimelineError:
    return TimelineError(f"{where}: {msg}")


def _number(value, where: str) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise _err(where, f"expected a number, got {value!r}")
    return float(value)


def _seconds(value, where: str) -> Fraction:
    try:
        return to_seconds(value)
    except ValueError as exc:
        raise _err(where, str(exc)) from exc


def _parse_keyframes(raw, where: str) -> tuple[Keyframe, ...]:
    if isinstance(raw, (int, float)) and not isinstance(raw, bool):
        raw = [{"frame": 0, "value": raw, "interp": "hold"}]
    if not isinstance(raw, list) or not raw:
        raise _err(where, "expected a number or a non-empty keyframe list")
    keys: list[Keyframe] = []
    seen: dict[int, Keyframe] = {}
    for i, item in enumerate(raw):
        loc = f"{where}[{i}]"
        if not isinstance(item, dict):
            raise _err(loc, f"expected an object, got {item!r}")
        frame = item.get("frame")
        if isinstance(frame, bool) or not isinstance(frame, int) or frame < 0:
            raise _err(loc, f"frame must be a non-negative integer, got {frame!r}")
        value = _number(item.get("value"), f"{loc}.value")
        if not 0.0 <= value <= 1.0:
            raise _err(loc, f"value must be within [0, 1], got {value}")
        interp = item.get("interp", "linear")
        if interp not in INTERP_MODES:
            raise _err(loc, f"interp must be one of {INTERP_MODES}, got {interp!r}")
        key = Keyframe(frame, value, interp)
        if frame in seen:
            prev = seen[frame]
            if prev.value != value or prev.interp != interp:
                raise _err(
                    loc,
                    f"duplicate keyframe at frame {frame} conflicts with "
                    f"earlier value={prev.value} interp={prev.interp!r}",
                )
            continue  # identical duplicate: keep the first
        seen[frame] = key
        keys.append(key)
    keys.sort(key=lambda k: k.frame)
    return tuple(keys)


def _parse_layer(raw, index: int) -> Layer:
    where = f"layers[{index}]"
    if not isinstance(raw, dict):
        raise _err(where, f"expected an object, got {raw!r}")
    lid = raw.get("id")
    if not isinstance(lid, str) or not lid:
        raise _err(where, f"id must be a non-empty string, got {lid!r}")
    where = f"layers[{index}]({lid})"
    asset = raw.get("asset")
    if not isinstance(asset, str) or not asset:
        raise _err(where, f"asset must be a non-empty string, got {asset!r}")
    start = _seconds(raw.get("start", 0), f"{where}.start")
    end = _seconds(raw.get("end"), f"{where}.end")
    if start < 0:
        raise _err(where, f"start must be >= 0, got {start}")
    if end < start:
        raise _err(where, f"end {end} is before start {start}")
    pos = raw.get("position", [0, 0])
    if not isinstance(pos, (list, tuple)) or len(pos) != 2:
        raise _err(where, f"position must be [x, y], got {pos!r}")
    x = _number(pos[0], f"{where}.position[0]")
    y = _number(pos[1], f"{where}.position[1]")
    scale = _number(raw.get("scale", 1.0), f"{where}.scale")
    if scale < 0:
        raise _err(where, f"scale must be >= 0, got {scale}")
    blend = raw.get("blend", "normal")
    if blend not in BLEND_MODES:
        raise _err(where, f"blend must be one of {BLEND_MODES}, got {blend!r}")
    opacity = _parse_keyframes(raw.get("opacity", 1.0), f"{where}.opacity")
    return Layer(lid, asset, start, end, x, y, scale, blend, opacity)


def _parse_beeps(raw, where: str) -> tuple[Beep, ...]:
    if raw is None:
        return ()
    if not isinstance(raw, list):
        raise _err(where, f"expected a list, got {raw!r}")
    beeps = []
    for i, item in enumerate(raw):
        loc = f"{where}[{i}]"
        if not isinstance(item, dict):
            raise _err(loc, f"expected an object, got {item!r}")
        t = _seconds(item.get("time"), f"{loc}.time")
        if t < 0:
            raise _err(loc, f"time must be >= 0, got {t}")
        freq = _number(item.get("freq", 880.0), f"{loc}.freq")
        if freq <= 0:
            raise _err(loc, f"freq must be > 0, got {freq}")
        dur = _seconds(item.get("duration", 0.08), f"{loc}.duration")
        if dur <= 0:
            raise _err(loc, f"duration must be > 0, got {dur}")
        beeps.append(Beep(t, freq, dur))
    return tuple(beeps)


def parse_timeline(data) -> Timeline:
    if not isinstance(data, dict):
        raise TimelineError(f"timeline root must be an object, got {data!r}")
    try:
        fps = parse_fps(data.get("fps"))
    except ValueError as exc:
        raise _err("fps", str(exc)) from exc
    width = data.get("width")
    height = data.get("height")
    for name, v in (("width", width), ("height", height)):
        if isinstance(v, bool) or not isinstance(v, int) or v <= 0:
            raise _err(name, f"must be a positive integer, got {v!r}")
    duration = _seconds(data.get("duration"), "duration")
    if duration < 0:
        raise _err("duration", f"must be >= 0, got {float(duration)}")
    raw_layers = data.get("layers", [])
    if not isinstance(raw_layers, list):
        raise _err("layers", f"expected a list, got {raw_layers!r}")
    layers = tuple(_parse_layer(l, i) for i, l in enumerate(raw_layers))
    ids = [l.id for l in layers]
    if len(set(ids)) != len(ids):
        dup = next(i for i in ids if ids.count(i) > 1)
        raise _err("layers", f"duplicate layer id {dup!r}")
    audio = data.get("audio", {})
    if not isinstance(audio, dict):
        raise _err("audio", f"expected an object, got {audio!r}")
    rate = audio.get("sample_rate", 48000)
    if isinstance(rate, bool) or not isinstance(rate, int) or rate <= 0:
        raise _err("audio.sample_rate", f"must be a positive integer, got {rate!r}")
    beeps = _parse_beeps(audio.get("beeps"), "audio.beeps")
    return Timeline(
        fps=fps,
        width=width,
        height=height,
        duration=duration,
        frames=frame_count(fps, duration),
        layers=layers,
        sample_rate=rate,
        beeps=beeps,
    )
