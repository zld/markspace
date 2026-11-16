"""Off-screen RGBA compositor.

Pixel math is done in float64 on NumPy arrays; the canvas is normalized
to uint8 RGBA exactly once per frame with deterministic round-half-to-even
so identical inputs always hash identically.
"""
from __future__ import annotations

import numpy as np

from .model import Clip, Timeline


class CompositeError(RuntimeError):
    """Raised when a frame cannot be composited."""


def normalize_rgba(canvas: np.ndarray) -> np.ndarray:
    """Convert a float canvas in [0, 1] to normalized uint8 RGBA bytes."""
    return np.ascontiguousarray(
        np.clip(np.rint(canvas * 255.0), 0.0, 255.0).astype(np.uint8)
    )


def _blend_region(dst: np.ndarray, src: np.ndarray, mode: str) -> np.ndarray:
    """Blend float RGBA ``src`` over ``dst`` using the W3C compositing model.

    co = As*(1-Ab)*Cs + As*Ab*B(Cb,Cs) + (1-As)*Ab*Cb
    Ao = As + Ab*(1-As)
    """
    sa = src[..., 3:4]
    da = dst[..., 3:4]
    cs = src[..., :3]
    cd = dst[..., :3]
    if mode == "source-over":
        mixed = cs
    elif mode == "multiply":
        mixed = cs * cd
    elif mode == "screen":
        mixed = cs + cd - cs * cd
    else:
        raise CompositeError(f"unsupported blend mode {mode!r}")
    out_a = sa + da * (1.0 - sa)
    co = sa * (1.0 - da) * cs + sa * da * mixed + (1.0 - sa) * da * cd
    out = np.empty_like(dst)
    np.divide(co, out_a, out=out[..., :3], where=out_a > 0.0)
    out[..., 3:4] = out_a
    return out


def _stamp(canvas: np.ndarray, clip: Clip, opacity: float, ox: int, oy: int) -> None:
    """Composite one clip image onto the float canvas in place."""
    img_h, img_w = clip.image.shape[:2]
    can_h, can_w = canvas.shape[:2]
    # Intersect the placed image rect with the canvas (off-canvas clipping).
    x0 = max(0, ox)
    y0 = max(0, oy)
    x1 = min(can_w, ox + img_w)
    y1 = min(can_h, oy + img_h)
    if x0 >= x1 or y0 >= y1:
        return  # fully off-canvas: nothing to draw
    src = clip.image[y0 - oy : y1 - oy, x0 - ox : x1 - ox].astype(np.float64) / 255.0
    src = src.copy()
    src[..., 3] *= opacity
    region = canvas[y0:y1, x0:x1]
    region[...] = _blend_region(region, src, clip.blend_mode)


def compose_frame(timeline: Timeline, frame_index: int) -> np.ndarray:
    """Composite a single frame and return normalized uint8 RGBA pixels."""
    if not 0 <= frame_index < timeline.frame_count:
        raise CompositeError(
            f"frame_index {frame_index} outside [0, {timeline.frame_count})"
        )
    canvas = np.zeros((timeline.height, timeline.width, 4), dtype=np.float64)
    for layer in timeline.layers:
        for clip in layer.clips:
            if clip.is_empty or not clip.active(frame_index):
                continue
            opacity, x, y = clip.sample(frame_index)
            if opacity <= 0.0:
                continue
            try:
                _stamp(canvas, clip, opacity, int(np.rint(x)), int(np.rint(y)))
            except CompositeError:
                raise
            except Exception as exc:  # pragma: no cover - defensive
                raise CompositeError(
                    f"frame {frame_index}: clip {clip.name!r} failed: {exc}"
                ) from exc
    return normalize_rgba(canvas)
