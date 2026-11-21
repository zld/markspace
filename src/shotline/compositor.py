"""NumPy layer compositor.

All pixel math is implemented here on numpy arrays; Pillow is never used for
blending or compositing.  Layers are painted in stable ``(z, original order)``
order with source-over compositing and one of four separable blend modes.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np

from .model import Keyframe, Layer, Timeline
from .timebase import FrameClock


class CompositorError(RuntimeError):
    """Raised when a frame cannot be rendered from the supplied assets."""


@dataclass(frozen=True)
class LayerState:
    """Interpolated state of one layer at a given point on the timeline."""

    x: float
    y: float
    scale: float
    opacity: float
    crop: tuple[float, float, float, float]


def interpolate(keyframes: tuple[Keyframe, ...], t: float) -> LayerState:
    """Linearly interpolate keyframes; clamp outside the keyframe span."""
    first, last = keyframes[0], keyframes[-1]
    if t <= first.t:
        kf = first
        return LayerState(kf.x, kf.y, kf.scale, kf.opacity, kf.crop)
    if t >= last.t:
        kf = last
        return LayerState(kf.x, kf.y, kf.scale, kf.opacity, kf.crop)
    nxt = next(k for k in keyframes if k.t > t)
    prev = max((k for k in keyframes if k.t <= t), key=lambda k: k.t)
    span = nxt.t - prev.t
    f = 0.0 if span == 0.0 else (t - prev.t) / span
    crop = tuple(p + (n - p) * f for p, n in zip(prev.crop, nxt.crop))
    return LayerState(
        x=prev.x + (nxt.x - prev.x) * f,
        y=prev.y + (nxt.y - prev.y) * f,
        scale=prev.scale + (nxt.scale - prev.scale) * f,
        opacity=prev.opacity + (nxt.opacity - prev.opacity) * f,
        crop=crop,  # type: ignore[arg-type]
    )


def _crop_source(src: np.ndarray, crop: tuple[float, float, float, float]) -> np.ndarray:
    h, w = src.shape[0:2]
    l, t, r, b = crop
    x0 = max(0, min(w - 1, int(round(l * w))))
    x1 = max(x0 + 1, min(w, int(round(r * w))))
    y0 = max(0, min(h - 1, int(round(t * h))))
    y1 = max(y0 + 1, min(h, int(round(b * h))))
    return np.ascontiguousarray(src[y0:y1, x0:x1])


def _resize_nearest(img: np.ndarray, out_w: int, out_h: int) -> np.ndarray:
    """Deterministic nearest-neighbour resample implemented with numpy."""
    if out_w <= 0 or out_h <= 0:
        raise CompositorError(f"illegal resize target {out_w}x{out_h}")
    h, w = img.shape[0:2]
    ix = np.floor(np.arange(out_w) * (w / out_w)).astype(np.int64)
    iy = np.floor(np.arange(out_h) * (h / out_h)).astype(np.int64)
    ix = np.clip(ix, 0, w - 1)
    iy = np.clip(iy, 0, h - 1)
    return np.ascontiguousarray(img[iy][:, ix])


def _blend_rgb(mode: str, s: np.ndarray, d: np.ndarray) -> np.ndarray:
    if mode == "normal":
        return s
    if mode == "multiply":
        return s * d
    if mode == "screen":
        return s + d - s * d
    if mode == "add":
        return np.minimum(s + d, 1.0)
    raise CompositorError(f"unsupported blend mode {mode!r}")


def _paint_layer(canvas: np.ndarray, layer_img: np.ndarray, state: LayerState,
                 blend: str) -> None:
    """Paint one transformed layer onto the canvas in place (source-over)."""
    ch, cw = canvas.shape[0:2]
    scaled = _resize_nearest(
        layer_img,
        max(1, int(round(layer_img.shape[1] * state.scale))),
        max(1, int(round(layer_img.shape[0] * state.scale))),
    )
    lh, lw = scaled.shape[0:2]
    px, py = int(round(state.x)), int(round(state.y))

    x0c, x1c = max(0, px), min(cw, px + lw)
    y0c, y1c = max(0, py), min(ch, py + lh)
    if x0c >= x1c or y0c >= y1c:
        return  # fully off-canvas (negative coordinates are simply clipped)
    x0l, y0l = x0c - px, y0c - py
    patch = scaled[y0l:y0l + (y1c - y0c), x0l:x0l + (x1c - x0c)].astype(np.float64) / 255.0
    target = canvas[y0c:y1c, x0c:x1c].astype(np.float64) / 255.0

    sa = patch[..., 3:4] * state.opacity
    da = target[..., 3:4]
    s_rgb, d_rgb = patch[..., :3], target[..., :3]
    b_rgb = _blend_rgb(blend, s_rgb, d_rgb)

    # W3C Porter-Duff source-over with a separable blend function.
    out_rgb = sa * (1.0 - da) * s_rgb + sa * da * b_rgb + (1.0 - sa) * da * d_rgb
    out_a = sa + da - sa * da
    safe_a = np.where(out_a > 1e-12, out_a, 1.0)
    out_rgb = np.where(out_a > 1e-12, out_rgb / safe_a, 0.0)

    result = np.concatenate([np.clip(out_rgb, 0.0, 1.0), np.clip(out_a, 0.0, 1.0)], axis=2)
    canvas[y0c:y1c, x0c:x1c] = np.rint(result * 255.0).astype(np.uint8)


def render_frame(timeline: Timeline, assets: dict[str, np.ndarray],
                 clock: FrameClock, index: int) -> np.ndarray:
    """Render frame ``index`` and return a contiguous RGBA uint8 canvas."""
    t = float(clock.pts_seconds(index))
    canvas = np.zeros((timeline.height, timeline.width, 4), dtype=np.uint8)
    bg = np.array(timeline.background, dtype=np.uint8).reshape(1, 1, 4)
    canvas[:] = bg

    for layer in timeline.layer_sort_key():
        src = assets.get(layer.id)
        if src is None:
            raise CompositorError(f"no asset loaded for layer {layer.id!r}")
        if src.ndim != 3 or src.shape[2] != 4:
            raise CompositorError(f"layer {layer.id!r}: asset must be HxWx4 RGBA")
        state = interpolate(layer.keyframes, t)
        prepared = _crop_source(src, state.crop)
        _paint_layer(canvas, prepared, state, layer.blend)
    return np.ascontiguousarray(canvas)


def preload_assets(timeline: Timeline, loader) -> dict[str, np.ndarray]:
    """Load every layer source once via ``loader(layer)``; failures propagate."""
    assets: dict[str, np.ndarray] = {}
    for layer in timeline.layers:
        img = loader(layer)
        if not isinstance(img, np.ndarray) or img.dtype != np.uint8 or img.ndim != 3:
            raise CompositorError(f"layer {layer.id!r}: loader returned a non-RGBA array")
        assets[layer.id] = np.ascontiguousarray(img)
    return assets
