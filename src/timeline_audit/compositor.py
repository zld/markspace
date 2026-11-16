"""RGBA compositing: source-over with normal/multiply/screen/add blends.

Math follows the CSS Compositing and Blending model in straight-alpha
space, which keeps semi-transparent edges from going dark:

    alpha_o = a_s + a_d * (1 - a_s)
    color_o = a_s*(1-a_d)*c_s + a_s*a_d*B(c_d, c_s) + (1-a_s)*a_d*c_d

where B is the blend function. All arithmetic is float64 in [0, 1];
results are clamped to [0, 1] and converted to uint8 with round-half-
to-even (numpy.rint), so output is deterministic across runs.
"""

from __future__ import annotations

import numpy as np

BLEND_MODES = ("normal", "multiply", "screen", "add")


def _blend(mode: str, cd: np.ndarray, cs: np.ndarray) -> np.ndarray:
    if mode == "normal":
        return cs
    if mode == "multiply":
        return cd * cs
    if mode == "screen":
        return 1.0 - (1.0 - cd) * (1.0 - cs)
    if mode == "add":
        return np.minimum(cd + cs, 1.0)
    raise ValueError(f"unknown blend mode {mode!r}")


def composite_region(
    canvas: np.ndarray,
    layer: np.ndarray,
    x0: int,
    y0: int,
    opacity: float,
    mode: str,
) -> None:
    """Composite ``layer`` onto ``canvas`` at integer offset in place.

    Handles layers extending past any canvas edge (clipped), zero-size
    layers (skipped) and zero/negative opacity (skipped).
    """
    if layer.size == 0 or layer.shape[0] == 0 or layer.shape[1] == 0:
        return
    if opacity <= 0.0:
        return
    opacity = min(opacity, 1.0)
    ch, cw = canvas.shape[:2]
    lh, lw = layer.shape[:2]
    dx0, dy0 = max(0, x0), max(0, y0)
    sx0, sy0 = max(0, -x0), max(0, -y0)
    w = min(cw - dx0, lw - sx0)
    h = min(ch - dy0, lh - sy0)
    if w <= 0 or h <= 0:
        return

    src = layer[sy0 : sy0 + h, sx0 : sx0 + w].astype(np.float64) / 255.0
    dst = canvas[dy0 : dy0 + h, dx0 : dx0 + w].astype(np.float64) / 255.0
    a_s = src[..., 3:4] * opacity
    a_d = dst[..., 3:4]
    c_s = src[..., :3]
    c_d = dst[..., :3]

    a_o = a_s + a_d * (1.0 - a_s)
    b = _blend(mode, c_d, c_s)
    num = a_s * (1.0 - a_d) * c_s + a_s * a_d * b + (1.0 - a_s) * a_d * c_d
    safe_a = np.where(a_o > 0.0, a_o, 1.0)
    c_o = np.where(a_o > 0.0, num / safe_a, 0.0)

    out = np.concatenate([c_o, a_o], axis=2)
    out = np.clip(out, 0.0, 1.0)
    canvas[dy0 : dy0 + h, dx0 : dx0 + w] = np.rint(out * 255.0).astype(np.uint8)


def scale_layer(layer: np.ndarray, scale: float) -> np.ndarray:
    """Resize an RGBA layer by ``scale`` with bilinear filtering.

    A scale of zero (or one collapsing to zero pixels) yields a 0x0
    array, which composite_region treats as a no-op.
    """
    from PIL import Image

    if scale <= 0.0:
        return np.zeros((0, 0, 4), dtype=np.uint8)
    h, w = layer.shape[:2]
    nw, nh = int(round(w * scale)), int(round(h * scale))
    if nw <= 0 or nh <= 0:
        return np.zeros((0, 0, 4), dtype=np.uint8)
    if (nw, nh) == (w, h):
        return layer
    im = Image.fromarray(layer, "RGBA").resize((nw, nh), Image.BILINEAR)
    return np.array(im, dtype=np.uint8)
