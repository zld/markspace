"""Deterministic demo-asset generation and sandboxed PNG loading.

Assets are plain RGBA numpy arrays.  Demo assets are synthesised from a seed so
any machine reproduces byte-identical pixels; local images are read through
Pillow but only when the resolved path stays inside the timeline directory.
"""

from __future__ import annotations

import zlib
from pathlib import Path

import numpy as np
from PIL import Image

from .model import Timeline, TimelineError


class AssetError(IOError):
    """Raised when an asset cannot be read or lies outside the sandbox."""


# ---------------------------------------------------------------------------
# Deterministic synthetic assets
# ---------------------------------------------------------------------------

def _rng(seed: int, salt: int) -> np.random.RandomState:
    return np.random.RandomState((seed * 2_654_435_761 + salt) & 0xFFFFFFFF)


def make_gradient_asset(width: int, height: int, seed: int, salt: int = 1) -> np.ndarray:
    """RGBA gradient with a deterministic checker overlay and solid alpha."""
    rng = _rng(seed, salt)
    base = rng.randint(0, 256, size=3, dtype=np.int64)
    yy, xx = np.mgrid[0:height, 0:width]
    fx = xx / max(width - 1, 1)
    fy = yy / max(height - 1, 1)
    img = np.zeros((height, width, 4), dtype=np.uint8)
    for c in range(3):
        ramp = base[c] * (1.0 - fx) + (255 - base[c]) * fy
        checker = ((xx // 8 + yy // 8) % 2) * 24
        img[..., c] = np.clip(ramp + checker, 0, 255).astype(np.uint8)
    img[..., 3] = 255
    return img


def make_badge_asset(width: int, height: int, seed: int, salt: int = 2) -> np.ndarray:
    """RGBA circle badge; pixels outside the disc are fully transparent."""
    rng = _rng(seed, salt)
    colour = rng.randint(40, 216, size=3).astype(np.uint8)
    yy, xx = np.mgrid[0:height, 0:width].astype(np.float64)
    cx, cy = (width - 1) / 2.0, (height - 1) / 2.0
    radius = min(width, height) / 2.0 - 1.0
    dist = np.sqrt((xx - cx) ** 2 + (yy - cy) ** 2)
    img = np.zeros((height, width, 4), dtype=np.uint8)
    inside = dist <= radius
    img[inside, 0:3] = colour
    # Soft anti-aliased edge for realistic partial-alpha coverage.
    edge = np.clip(radius + 0.5 - dist, 0.0, 1.0)
    img[..., 3] = (edge * 255).astype(np.uint8)
    return img


def make_stripe_asset(width: int, height: int, seed: int, salt: int = 3) -> np.ndarray:
    """RGBA diagonal stripes with alternating alpha bands."""
    rng = _rng(seed, salt)
    c1 = rng.randint(0, 256, size=3).astype(np.uint8)
    c2 = rng.randint(0, 256, size=3).astype(np.uint8)
    yy, xx = np.mgrid[0:height, 0:width]
    band = ((xx + yy) // 12) % 2 == 0
    img = np.zeros((height, width, 4), dtype=np.uint8)
    img[band, 0:3] = c1
    img[~band, 0:3] = c2
    img[band, 3] = 255
    img[~band, 3] = 110
    return img


GENERATORS = {
    "demo:gradient": make_gradient_asset,
    "demo:badge": make_badge_asset,
    "demo:stripe": make_stripe_asset,
}


def is_generated(source: str) -> bool:
    return source in GENERATORS


# ---------------------------------------------------------------------------
# Sandboxed local files
# ---------------------------------------------------------------------------

def _resolve_within(base_dir: Path, source: str) -> Path:
    candidate = (base_dir / source).resolve()
    try:
        candidate.relative_to(base_dir.resolve())
    except ValueError as e:
        raise AssetError(
            f"asset {source!r} resolves outside the timeline directory {base_dir}"
        ) from e
    return candidate


def load_png(path: Path) -> np.ndarray:
    """Read a local PNG as a contiguous RGBA uint8 array."""
    try:
        with Image.open(path) as im:
            return np.asarray(im.convert("RGBA"), dtype=np.uint8).copy()
    except FileNotFoundError as e:
        raise AssetError(f"asset not found: {path}") from e
    except (OSError, ValueError) as e:
        raise AssetError(f"failed to decode image {path}: {e}") from e


def load_asset(source: str, timeline: Timeline, *, seed: int = 0,
               salt: int = 0) -> np.ndarray:
    """Resolve a layer source to an RGBA array.

    ``demo:*`` names generate deterministic pixels; anything else is treated
    as a relative path and must stay inside ``timeline.base_dir``.
    """
    if is_generated(source):
        # Generated assets use a fixed intrinsic size; callers may crop/scale.
        gen = GENERATORS[source]
        stable_salt = salt if salt else zlib.crc32(source.encode("utf-8")) & 0xFFFF
        return gen(64, 64, seed=seed, salt=stable_salt)
    path = _resolve_within(timeline.base_dir, source)
    return load_png(path)


def write_png(path: Path, pixels: np.ndarray) -> None:
    """Helper used by tests/demo fixtures to materialise a local PNG."""
    if pixels.ndim != 3 or pixels.shape[2] != 4:
        raise AssetError("write_png expects an HxWx4 RGBA array")
    path.parent.mkdir(parents=True, exist_ok=True)
    Image.fromarray(pixels.astype(np.uint8), mode="RGBA").save(path, format="PNG")
