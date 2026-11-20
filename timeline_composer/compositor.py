"""Single-frame layered compositing.

The compositor owns no file I/O beyond asking an :class:`AssetStore` for
decoded PNGs; it produces a fresh straight-alpha RGBA8 ``numpy`` array for a
given frame index. Rendering is fully deterministic:

* layers are drawn bottom-to-top in ``(order, declaration)`` sequence;
* positions and scales come from exact rational keyframe tracks and are
  quantised with one rule (half-up to the nearest pixel / integer dimension);
* scaling happens on *premultiplied linear* colour, so transparent edges do
  not pick up fringe colours;
* blend modes run in linear light and the result is encoded once at the end.

Off-canvas geometry, fully transparent layers, zero scales, and timelines
with no active layers are all handled without special-casing the caller.
"""

from __future__ import annotations

import math
from dataclasses import dataclass
from fractions import Fraction

import numpy as np
from PIL import Image

from .assets import AssetStore
from .color import blend, encode_frame, premultiply, scale_alpha
from .schema import Layer, Timeline

_Resample = Image.Resampling.BILINEAR


class CompositorError(RuntimeError):
    """Raised when a frame cannot be composed."""


@dataclass
class FrameCompositor:
    """Compose frames of ``timeline`` using PNGs from ``assets``.

    The canvas starts fully transparent; with no active layers a frame is
    therefore all zeroes, which makes "nothing to draw" hash-stable.
    """

    timeline: Timeline
    assets: AssetStore

    def __post_init__(self) -> None:
        self._width = self.timeline.canvas.width
        self._height = self.timeline.canvas.height
        self._order = self.timeline.draw_order()

    def compose(self, frame_index: int) -> np.ndarray:
        """Return the straight-alpha RGBA8 frame at ``frame_index``."""
        clock = self.timeline.clock
        timestamp = clock.pts(frame_index)
        rgb = np.zeros((self._height, self._width, 3), dtype=np.float32)
        alpha = np.zeros((self._height, self._width), dtype=np.float32)
        for layer in self._order:
            if not layer.is_active(timestamp):
                continue
            rgb, alpha = self._draw_layer(layer, timestamp, rgb, alpha)
        return encode_frame(rgb, alpha)

    # -- internals ---------------------------------------------------------

    def _draw_layer(
        self,
        layer: Layer,
        timestamp: Fraction,
        canvas_rgb: np.ndarray,
        canvas_alpha: np.ndarray,
    ) -> tuple[np.ndarray, np.ndarray]:
        opacity = _clamp01(layer.opacity.evaluate(timestamp))
        if opacity <= 0:
            return canvas_rgb, canvas_alpha

        scale_x, scale_y = _scale_pair(layer.scale.evaluate(timestamp))
        if scale_x <= 0 or scale_y <= 0:
            return canvas_rgb, canvas_alpha

        position = layer.position.evaluate(timestamp)
        if not isinstance(position, tuple):
            raise CompositorError(f"layer {layer.id!r} position must resolve to x/y")
        pos_x, pos_y = position

        source = self.assets.load(layer.source)
        source_h, source_w = source.shape[:2]
        target_w = _quantise_dimension(source_w, scale_x)
        target_h = _quantise_dimension(source_h, scale_y)
        if target_w <= 0 or target_h <= 0:
            return canvas_rgb, canvas_alpha

        left = _quantise_coordinate(pos_x)
        top = _quantise_coordinate(pos_y)
        dest_box = _intersect((left, top, target_w, target_h), self._width, self._height)
        if dest_box is None:
            return canvas_rgb, canvas_alpha
        dst_x0, dst_y0, dst_x1, dst_y1 = dest_box

        src_rgb, src_alpha = premultiply(source)
        if (target_w, target_h) != (source_w, source_h):
            src_rgb, src_alpha = _resize_premultiplied(
                src_rgb, src_alpha, (target_w, target_h)
            )
        src_rgb, src_alpha = scale_alpha(src_rgb, src_alpha, opacity)

        # Map the destination clip back onto the resized source rectangle.
        src_x0 = dst_x0 - left
        src_y0 = dst_y0 - top
        src_x1 = src_x0 + (dst_x1 - dst_x0)
        src_y1 = src_y0 + (dst_y1 - dst_y0)
        patch_rgb = src_rgb[src_y0:src_y1, src_x0:src_x1]
        patch_alpha = src_alpha[src_y0:src_y1, src_x0:src_x1]

        if not np.any(patch_alpha > 0.0):
            return canvas_rgb, canvas_alpha

        region_rgb = canvas_rgb[dst_y0:dst_y1, dst_x0:dst_x1]
        region_alpha = canvas_alpha[dst_y0:dst_y1, dst_x0:dst_x1]
        new_rgb, new_alpha = blend(
            region_rgb, region_alpha, patch_rgb, patch_alpha, layer.blend
        )
        canvas_rgb[dst_y0:dst_y1, dst_x0:dst_x1] = new_rgb
        canvas_alpha[dst_y0:dst_y1, dst_x0:dst_x1] = new_alpha
        return canvas_rgb, canvas_alpha


def _resize_premultiplied(
    rgb: np.ndarray, alpha: np.ndarray, size: tuple[int, int]
) -> tuple[np.ndarray, np.ndarray]:
    """Resize premultiplied linear RGB + alpha with bilinear filtering."""
    width, height = size
    channels = np.empty((height, width, 4), dtype=np.float32)
    stacked = np.concatenate([rgb, alpha[..., None]], axis=2)
    for channel in range(4):
        plane = Image.fromarray(np.ascontiguousarray(stacked[..., channel], dtype=np.float32))
        resized = plane.resize((width, height), resample=_Resample)
        channels[..., channel] = np.asarray(resized, dtype=np.float32)
    return channels[..., :3], channels[..., 3]


def _intersect(
    box: tuple[int, int, int, int], canvas_w: int, canvas_h: int
) -> tuple[int, int, int, int] | None:
    """Clip ``(left, top, w, h)`` to the canvas, returning exclusive bounds."""
    left, top, width, height = box
    x0 = max(left, 0)
    y0 = max(top, 0)
    x1 = min(left + width, canvas_w)
    y1 = min(top + height, canvas_h)
    if x0 >= x1 or y0 >= y1:
        return None
    return x0, y0, x1, y1


def _quantise_coordinate(value: Fraction) -> int:
    """Round a rational coordinate half-up to an integer pixel offset."""
    return int(math.floor(Fraction(value) + Fraction(1, 2)))


def _quantise_dimension(source: int, scale: Fraction) -> int:
    exact = Fraction(source) * Fraction(scale)
    if exact <= 0:
        return 0
    return max(1, int(math.floor(exact + Fraction(1, 2))))


def _scale_pair(value: Fraction | tuple[Fraction, Fraction]) -> tuple[Fraction, Fraction]:
    if isinstance(value, tuple):
        return value
    return value, value


def _clamp01(value: Fraction | tuple[Fraction, Fraction]) -> float:
    if isinstance(value, tuple):
        raise CompositorError("opacity track must resolve to a scalar")
    numeric = float(Fraction(value))
    if not math.isfinite(numeric):
        raise CompositorError("opacity must be finite")
    return min(1.0, max(0.0, numeric))


__all__ = ["CompositorError", "FrameCompositor"]
