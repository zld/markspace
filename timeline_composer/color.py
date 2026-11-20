"""sRGB <-> linear-light conversion and premultiplied-alpha blending.

All compositing happens in linear light with premultiplied alpha:

* 8-bit sRGB inputs are decoded through a 256-entry LUT.
* Layers are premultiplied (RGB * alpha) while still linear.
* Blend modes operate on unpremultiplied colours using the W3C compositing
  formula ``Co = (1 - Ab) * Cs + Ab * B(Cb, Cs)`` and are re-premultiplied
  against the destination afterwards. This keeps transparent edges correct
  (no dark or bright fringes).
* The final framebuffer is encoded with a single rounding rule: half-up
  (``floor(x * scale + 0.5)``) everywhere, via a deterministic 16-bit LUT.
"""

from __future__ import annotations

import math

import numpy as np

# Decode table: sRGB 8-bit code -> linear-light float32.
SRGB_TO_LINEAR = np.array(
    [
        (channel / 255.0 / 12.92)
        if channel / 255.0 <= 0.04045
        else (((channel / 255.0 + 0.055) / 1.055) ** 2.4)
        for channel in range(256)
    ],
    dtype=np.float32,
)

def _srgb_encode(linear: float) -> float:
    if linear <= 0.0031308:
        return 12.92 * linear
    return 1.055 * (linear ** (1.0 / 2.4)) - 0.055


_LUT_STEPS = 65_536
# Encode table: linear-light value quantised to 1/65536 -> sRGB 8-bit code.
# Built once at import so runtime encoding needs no transcendental maths.
LINEAR_TO_SRGB8 = np.array(
    [
        min(255, int(math.floor(_srgb_encode(index / _LUT_STEPS) * 255.0 + 0.5)))
        for index in range(_LUT_STEPS + 1)
    ],
    dtype=np.uint8,
)


def srgb8_to_linear(values: np.ndarray) -> np.ndarray:
    """Decode 8-bit sRGB values (any shape) to linear float32."""
    return SRGB_TO_LINEAR[np.asarray(values, dtype=np.uint8)]


def linear_to_srgb8(values: np.ndarray) -> np.ndarray:
    """Encode linear-light values to 8-bit sRGB with half-up rounding."""
    clipped = np.clip(np.asarray(values, dtype=np.float32), 0.0, 1.0)
    index = np.floor(clipped * _LUT_STEPS + 0.5).astype(np.int32)
    return LINEAR_TO_SRGB8[np.minimum(index, _LUT_STEPS)]


def to_uint8(alpha: np.ndarray) -> np.ndarray:
    """Quantise linear alpha to 8-bit with the same half-up rule."""
    clipped = np.clip(np.asarray(alpha, dtype=np.float32), 0.0, 1.0)
    return np.floor(clipped * 255.0 + 0.5).astype(np.uint8)


def premultiply(rgba_u8: np.ndarray) -> tuple[np.ndarray, np.ndarray]:
    """Convert straight RGBA8 to linear premultiplied RGB and linear alpha.

    Returns ``(rgb_premultiplied, alpha)`` with shapes ``(h, w, 3)`` and
    ``(h, w)``, both float32.
    """
    array = np.asarray(rgba_u8, dtype=np.uint8)
    if array.ndim != 3 or array.shape[2] != 4:
        raise ValueError("premultiply expects an (h, w, 4) RGBA array")
    alpha = array[..., 3].astype(np.float32) / np.float32(255.0)
    linear = srgb8_to_linear(array[..., :3])
    return linear * alpha[..., None], alpha


def scale_alpha(
    rgb_premultiplied: np.ndarray, alpha: np.ndarray, opacity: float
) -> tuple[np.ndarray, np.ndarray]:
    """Apply a layer opacity uniformly to premultiplied colour and alpha."""
    factor = np.float32(min(1.0, max(0.0, opacity)))
    return rgb_premultiplied * factor, alpha * factor


def blend(
    dst_rgb: np.ndarray,
    dst_alpha: np.ndarray,
    src_rgb: np.ndarray,
    src_alpha: np.ndarray,
    mode: str,
) -> tuple[np.ndarray, np.ndarray]:
    """Blend premultiplied ``src`` over premultiplied ``dst`` in linear light.

    All arguments share the same ``(h, w)`` spatial shape; returns new
    premultiplied RGB and alpha arrays. ``mode`` is ``normal``, ``multiply``
    or ``screen``.
    """
    if mode not in ("normal", "multiply", "screen"):
        raise ValueError(f"unsupported blend mode {mode!r}")
    src_a = src_alpha[..., None]
    dst_a = dst_alpha[..., None]
    if mode == "normal":
        out_rgb = src_rgb + dst_rgb * (1.0 - src_a)
        out_alpha = src_alpha + dst_alpha * (1.0 - src_alpha)
        return out_rgb, out_alpha

    # Unpremultiply the destination where it has coverage; elsewhere treat
    # destination colour as zero so the blend degrades to plain source-over.
    safe_dst_a = np.where(dst_alpha > 0.0, dst_alpha, np.float32(1.0))[..., None]
    dst_straight = np.where(dst_a > 0.0, dst_rgb / safe_dst_a, np.float32(0.0))
    safe_src_a = np.where(src_alpha > 0.0, src_alpha, np.float32(1.0))[..., None]
    src_straight = np.where(src_a > 0.0, src_rgb / safe_src_a, np.float32(0.0))

    if mode == "multiply":
        blended = dst_straight * src_straight
    else:  # screen
        blended = dst_straight + src_straight - dst_straight * src_straight

    composited = (1.0 - dst_a) * src_straight + dst_a * blended
    out_rgb = src_a * composited + (1.0 - src_a) * dst_rgb
    out_alpha = src_alpha + dst_alpha * (1.0 - src_alpha)
    return out_rgb, out_alpha


def encode_frame(rgb_premultiplied: np.ndarray, alpha: np.ndarray) -> np.ndarray:
    """Convert a linear premultiplied canvas to straight RGBA8.

    Fully transparent pixels encode to ``(0, 0, 0, 0)`` so the hash does not
    depend on leftover colour under invisible pixels.
    """
    rgb = np.asarray(rgb_premultiplied, dtype=np.float32)
    a = np.asarray(alpha, dtype=np.float32)
    covered = a > 0.0
    safe_a = np.where(covered, a, np.float32(1.0))[..., None]
    straight = np.where(covered[..., None], rgb / safe_a, np.float32(0.0))
    out = np.empty(rgb.shape[:2] + (4,), dtype=np.uint8)
    out[..., :3] = linear_to_srgb8(straight)
    out[..., 3] = to_uint8(a)
    out[~covered] = 0
    return out


__all__ = [
    "LINEAR_TO_SRGB8",
    "SRGB_TO_LINEAR",
    "blend",
    "encode_frame",
    "linear_to_srgb8",
    "premultiply",
    "scale_alpha",
    "srgb8_to_linear",
    "to_uint8",
]
