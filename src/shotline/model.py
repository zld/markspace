"""Timeline parsing and validation.

A timeline is JSON describing a canvas, a rational frame rate, a duration in
seconds, ordered layers with keyframes, and audio cue points.  Everything that
can make downstream rendering ambiguous is rejected here with
:class:`TimelineError` so the compositor only ever sees well-formed input.
"""

from __future__ import annotations

import json
import math
from dataclasses import dataclass, field
from fractions import Fraction
from pathlib import Path
from typing import Any

MAX_SIDE = 4096
BLEND_MODES = ("normal", "multiply", "screen", "add")


class TimelineError(ValueError):
    """Raised when a timeline document is structurally or numerically invalid."""


def _require(obj: Any, key: str, kind: type | tuple[type, ...], ctx: str) -> Any:
    if not isinstance(obj, dict) or key not in obj:
        raise TimelineError(f"{ctx}: missing required field {key!r}")
    val = obj[key]
    if not isinstance(val, kind):
        raise TimelineError(f"{ctx}.{key}: expected {kind}, got {type(val).__name__}")
    return val


def _finite_number(obj: dict, key: str, ctx: str, *, positive: bool = False,
                    non_negative: bool = False) -> float:
    val = _require(obj, key, (int, float), ctx)
    if isinstance(val, bool) or not math.isfinite(val):
        raise TimelineError(f"{ctx}.{key}: must be a finite number")
    if positive and val <= 0:
        raise TimelineError(f"{ctx}.{key}: must be > 0")
    if non_negative and val < 0:
        raise TimelineError(f"{ctx}.{key}: must be >= 0")
    return float(val)


def _fraction(raw: Any, ctx: str) -> Fraction:
    if isinstance(raw, int) and not isinstance(raw, bool) and raw > 0:
        return Fraction(raw, 1)
    if isinstance(raw, dict):
        num = _require(raw, "numerator", int, ctx)
        den = _require(raw, "denominator", int, ctx)
        if isinstance(num, bool) or isinstance(den, bool) or den <= 0 or num <= 0:
            raise TimelineError(f"{ctx}: rational parts must be positive integers")
        return Fraction(num, den)
    if isinstance(raw, str) and "/" in raw:
        n_s, d_s = raw.split("/", 1)
        try:
            num, den = int(n_s), int(d_s)
        except ValueError:
            pass
        else:
            if num > 0 and den > 0:
                return Fraction(num, den)
    raise TimelineError(f"{ctx}: frame rate must be a positive int or rational object")


@dataclass(frozen=True)
class Keyframe:
    t: float                      # seconds
    x: float
    y: float
    scale: float
    opacity: float                # 0..1
    crop: tuple[float, float, float, float]  # left, top, right, bottom fractions 0..1


@dataclass(frozen=True)
class Layer:
    id: str
    source: str
    z: int
    blend: str
    keyframes: tuple[Keyframe, ...]
    order: int                    # original position in the layer list


@dataclass(frozen=True)
class Cue:
    sample_rate: int
    sample_index: int
    layer_id: str | None


@dataclass(frozen=True)
class Timeline:
    width: int
    height: int
    fps: Fraction
    duration: float
    background: tuple[int, int, int, int]
    layers: tuple[Layer, ...]
    cues: tuple[Cue, ...]
    base_dir: Path = field(default_factory=Path.cwd)

    def layer_sort_key(self) -> list[Layer]:
        return sorted(self.layers, key=lambda l: (l.z, l.order))


def _parse_crop(raw: Any, ctx: str) -> tuple[float, float, float, float]:
    if raw is None:
        return (0.0, 0.0, 1.0, 1.0)
    if not isinstance(raw, (list, tuple)) or len(raw) != 4:
        raise TimelineError(f"{ctx}: crop must be four numbers [l,t,r,b]")
    out = tuple(float(v) for v in raw)
    if any(not math.isfinite(v) for v in out):
        raise TimelineError(f"{ctx}: crop values must be finite")
    l, t, r, b = out
    if not (0.0 <= l < r <= 1.0 and 0.0 <= t < b <= 1.0):
        raise TimelineError(f"{ctx}: require 0 <= left < right <= 1 and 0 <= top < bottom <= 1")
    return out  # type: ignore[return-value]


def _parse_keyframes(raw: Any, ctx: str, duration: float) -> tuple[Keyframe, ...]:
    if not isinstance(raw, list) or not raw:
        raise TimelineError(f"{ctx}: keyframes must be a non-empty list")
    frames: list[Keyframe] = []
    seen: set[float] = set()
    for i, kf in enumerate(raw):
        kctx = f"{ctx}[{i}]"
        if not isinstance(kf, dict):
            raise TimelineError(f"{kctx}: keyframe must be an object")
        t = _finite_number(kf, "t", kctx, non_negative=True)
        if t > duration:
            raise TimelineError(f"{kctx}.t: keyframe at {t}s exceeds duration {duration}s")
        if t in seen:
            raise TimelineError(f"{kctx}.t: duplicate keyframe time {t}")
        seen.add(t)
        x = _finite_number(kf, "x", kctx) if "x" in kf else 0.0
        y = _finite_number(kf, "y", kctx) if "y" in kf else 0.0
        scale = _finite_number(kf, "scale", kctx, positive=True) if "scale" in kf else 1.0
        opacity = _finite_number(kf, "opacity", kctx, non_negative=True) if "opacity" in kf else 1.0
        if opacity > 1.0:
            raise TimelineError(f"{kctx}.opacity: must be within 0..1")
        frames.append(Keyframe(t, x, y, scale, opacity, _parse_crop(kf.get("crop"), kctx)))
    frames.sort(key=lambda k: k.t)
    return tuple(frames)


def _parse_layer(raw: Any, idx: int, duration: float) -> Layer:
    ctx = f"layers[{idx}]"
    if not isinstance(raw, dict):
        raise TimelineError(f"{ctx}: layer must be an object")
    lid = _require(raw, "id", str, ctx)
    source = _require(raw, "source", str, ctx)
    if not source:
        raise TimelineError(f"{ctx}.source: must not be empty")
    z = _require(raw, "z", int, ctx)
    if isinstance(z, bool):
        raise TimelineError(f"{ctx}.z: boolean is not a valid z value")
    blend = raw.get("blend", "normal")
    if blend not in BLEND_MODES:
        raise TimelineError(f"{ctx}.blend: unknown mode {blend!r}; expected one of {BLEND_MODES}")
    keyframes = _parse_keyframes(raw.get("keyframes"), ctx, duration)
    return Layer(lid, source, z, blend, keyframes, idx)


def _parse_cues(raw: Any, duration: float) -> tuple[Cue, ...]:
    if raw is None:
        return ()
    if not isinstance(raw, list):
        raise TimelineError("cues: must be a list")
    cues: list[Cue] = []
    seen_samples: set[tuple[int, int]] = set()
    for i, c in enumerate(raw):
        ctx = f"cues[{i}]"
        if not isinstance(c, dict):
            raise TimelineError(f"{ctx}: cue must be an object")
        sr = _require(c, "sample_rate", int, ctx)
        si = _require(c, "sample_index", int, ctx)
        if isinstance(sr, bool) or isinstance(si, bool) or sr <= 0 or si < 0:
            raise TimelineError(f"{ctx}: sample_rate must be > 0 and sample_index >= 0")
        if si / sr > duration + 1e-12:
            raise TimelineError(
                f"{ctx}: cue at sample {si} ({si / sr:.6f}s) is outside the timeline duration {duration}s"
            )
        key = (sr, si)
        if key in seen_samples:
            raise TimelineError(f"{ctx}: duplicate cue at sample_index {si} (rate {sr})")
        seen_samples.add(key)
        layer_id = c.get("layer_id")
        if layer_id is not None and not isinstance(layer_id, str):
            raise TimelineError(f"{ctx}.layer_id: must be a string")
        cues.append(Cue(sr, si, layer_id))
    return tuple(cues)


def _parse_background(raw: Any) -> tuple[int, int, int, int]:
    if raw is None:
        return (0, 0, 0, 0)
    if not isinstance(raw, (list, tuple)) or len(raw) not in (3, 4):
        raise TimelineError("background: must be [r,g,b] or [r,g,b,a]")
    vals = [int(v) for v in raw]
    if any(isinstance(v, bool) for v in raw) or any(not 0 <= v <= 255 for v in vals):
        raise TimelineError("background: channels must be integers in 0..255")
    if len(vals) == 3:
        vals.append(255)
    return tuple(vals)  # type: ignore[return-value]


def parse_timeline(doc: dict[str, Any], base_dir: Path | None = None) -> Timeline:
    """Validate a decoded JSON document and return an immutable :class:`Timeline`."""
    if not isinstance(doc, dict):
        raise TimelineError("timeline: root must be a JSON object")
    width = _require(doc, "width", int, "timeline")
    height = _require(doc, "height", int, "timeline")
    if isinstance(width, bool) or isinstance(height, bool):
        raise TimelineError("timeline: width/height must be integers")
    if not (1 <= width <= MAX_SIDE and 1 <= height <= MAX_SIDE):
        raise TimelineError(f"timeline: canvas sides must be in 1..{MAX_SIDE} px")
    fps = _fraction(_require(doc, "fps", (int, dict, str), "timeline"), "timeline.fps")
    duration = _finite_number(doc, "duration", "timeline", positive=True)
    raw_layers = _require(doc, "layers", list, "timeline")
    layers = tuple(_parse_layer(l, i, duration) for i, l in enumerate(raw_layers))
    ids = [l.id for l in layers]
    if len(set(ids)) != len(ids):
        raise TimelineError("layers: layer ids must be unique")
    cues = _parse_cues(doc.get("cues"), duration)
    cue_layers = {c.layer_id for c in cues if c.layer_id is not None}
    unknown = cue_layers - set(ids)
    if unknown:
        raise TimelineError(f"cues: reference unknown layer id(s) {sorted(unknown)}")
    return Timeline(
        width=width,
        height=height,
        fps=fps,
        duration=duration,
        background=_parse_background(doc.get("background")),
        layers=layers,
        cues=cues,
        base_dir=(base_dir or Path.cwd()).resolve(),
    )


def load_timeline(path: str | Path) -> Timeline:
    """Read and validate a timeline JSON file; its directory bounds image paths."""
    p = Path(path).resolve()
    try:
        doc = json.loads(p.read_text(encoding="utf-8"))
    except FileNotFoundError as e:
        raise TimelineError(f"timeline file not found: {p}") from e
    except json.JSONDecodeError as e:
        raise TimelineError(f"timeline is not valid JSON: {e}") from e
    return parse_timeline(doc, base_dir=p.parent)
