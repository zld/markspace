"""Integer blend kernels.

All compositing happens in a premultiplied-alpha integer domain (channels and
alpha in 0..255, stored as uint16) so results are exact and identical on every
supported platform. Straight-alpha PNG input is premultiplied on entry and the
final canvas is converted back to straight RGBA on exit; transparent pixels
therefore cannot leak their RGB into edges.

Definitions per layer, with P = premultiplied source RGB, A = source alpha,
Cp/Ca = premultiplied canvas RGB/alpha, inv = 255 - A (all integer, // is
floor division):

  normal:   out_P = P + Cp*inv//255        out_A = A + Ca*inv//255
  add:      out_P = min(255, P + Cp)       out_A = min(255, A + Ca)
  multiply: out_P = P*Cp//255 + Cp*inv//255  out_A = A + Ca*inv//255
"""

from __future__ import annotations

import numpy as np

from .model import BLEND_MODES


def premultiply(src_rgb: np.ndarray, alpha: np.ndarray) -> np.ndarray:
    """Straight RGB (h,w,3) uint16 + alpha (h,w) uint16 -> premultiplied RGB."""
    return (src_rgb * alpha[..., None]) // 255


def composite(
    canvas_p: np.ndarray,
    canvas_a: np.ndarray,
    src_rgb: np.ndarray,
    alpha: np.ndarray,
    mode: str,
) -> tuple[np.ndarray, np.ndarray]:
    """Composite one layer onto the premultiplied canvas, in place semantics.

    canvas_p: (h,w,3) uint16 premultiplied; canvas_a: (h,w) uint16.
    src_rgb: (h,w,3) uint16 straight; alpha: (h,w) uint16 effective alpha.
    Returns new (canvas_p, canvas_a); inputs are not mutated.
    """
    if mode not in BLEND_MODES:
        raise ValueError(f"unknown blend mode: {mode}")
    p = premultiply(src_rgb, alpha)
    if mode == "normal":
        inv = 255 - alpha
        out_p = p + (canvas_p * inv[..., None]) // 255
        out_a = alpha + (canvas_a * inv) // 255
    elif mode == "add":
        out_p = np.minimum(255, p + canvas_p)
        out_a = np.minimum(255, alpha + canvas_a)
    else:  # multiply
        inv = 255 - alpha
        out_p = (p * canvas_p) // 255 + (canvas_p * inv[..., None]) // 255
        out_a = alpha + (canvas_a * inv) // 255
    return out_p.astype(np.uint16), out_a.astype(np.uint16)


def unpremultiply(canvas_p: np.ndarray, canvas_a: np.ndarray) -> np.ndarray:
    """Premultiplied canvas -> straight RGBA uint8 (h,w,4).

    Fully transparent pixels yield RGB 0, so hidden RGB data cannot
    contaminate the output.
    """
    h, w = canvas_a.shape
    rgb = np.zeros((h, w, 3), dtype=np.uint16)
    np.floor_divide(
        canvas_p * 255,
        canvas_a[..., None],
        out=rgb,
        where=canvas_a[..., None] > 0,
    )
    out = np.empty((h, w, 4), dtype=np.uint8)
    out[..., :3] = np.minimum(255, rgb)
    out[..., 3] = canvas_a
    return out
