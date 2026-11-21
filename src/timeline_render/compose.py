"""Deterministic per-frame compositor.

The exporter and every caller share `compose_frame`; there is no second
compositing path, so a digest of exported bytes proves what the compositor
produced.
"""

from __future__ import annotations

import math
from fractions import Fraction

import numpy as np
from PIL import Image

from .blend import composite, unpremultiply
from .model import ComposeError, InputError, Layer, Timeline


def opacity_at(layer: Layer, frame: int) -> Fraction:
    """Opacity of a layer at an integer frame, in [0, 1].

    Keyframes interpolate linearly by frame; outside the keyframe range the
    endpoint values hold.
    """
    kfs = layer.keyframes
    if not kfs:
        return layer.opacity
    if frame <= kfs[0].frame:
        return kfs[0].value
    if frame >= kfs[-1].frame:
        return kfs[-1].value
    for a, b in zip(kfs, kfs[1:]):
        if a.frame <= frame <= b.frame:
            t = Fraction(frame - a.frame, b.frame - a.frame)
            return a.value + (b.value - a.value) * t
    raise ComposeError(f"keyframe lookup failed at frame {frame}")


def opacity_byte(layer: Layer, frame: int) -> int:
    """Opacity quantized to 0..255, rounding half up, exactly."""
    v = opacity_at(layer, frame)
    return math.floor(v * 255 + Fraction(1, 2))


def load_images(
    timeline: Timeline, width: int, height: int
) -> dict[str, np.ndarray]:
    """Load every layer PNG once; missing files or wrong sizes are input errors."""
    images: dict[str, np.ndarray] = {}
    for i, layer in enumerate(timeline.layers):
        path = timeline.base_dir / layer.png
        field = f"layers[{i}].png"
        if not path.is_file():
            raise InputError(field, f"image not found: {layer.png}")
        try:
            with Image.open(path) as im:
                if im.size != (width, height):
                    raise InputError(
                        field,
                        f"image size {im.size[0]}x{im.size[1]} does not match "
                        f"canvas {width}x{height}",
                    )
                images[layer.name] = np.asarray(im.convert("RGBA"), dtype=np.uint8)
        except InputError:
            raise
        except Exception as exc:  # corrupt PNG etc.
            raise InputError(field, f"cannot read PNG: {exc}") from exc
    return images


def compose_frame(
    timeline: Timeline,
    images: dict[str, np.ndarray],
    width: int,
    height: int,
    frame: int,
) -> bytes:
    """Compose one frame and return its RGBA bytes (deterministic)."""
    if not 0 <= frame < timeline.frame_count:
        raise ComposeError(f"frame {frame} outside [0, {timeline.frame_count})")
    canvas_p = np.zeros((height, width, 3), dtype=np.uint16)
    canvas_a = np.zeros((height, width), dtype=np.uint16)
    ordered = sorted(timeline.layers, key=lambda l: (l.z, l.order))
    for layer in ordered:
        if not layer.start_frame <= frame < layer.end_frame:
            continue
        img = images[layer.name]
        src_rgb = img[..., :3].astype(np.uint16)
        alpha = (img[..., 3].astype(np.uint16) * opacity_byte(layer, frame)) // 255
        canvas_p, canvas_a = composite(
            canvas_p, canvas_a, src_rgb, alpha, layer.blend
        )
    rgba = unpremultiply(canvas_p, canvas_a)
    return rgba.tobytes()
