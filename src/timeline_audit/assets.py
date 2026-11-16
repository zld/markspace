"""Sample asset generation and safe local PNG loading.

Demo assets are synthesized with NumPy (no network, no external files).
load_png() only accepts PNG paths that stay inside the timeline's asset
directory; anything escaping via ".." or symlinks is rejected.
"""

from __future__ import annotations

from pathlib import Path

import numpy as np
from PIL import Image


class AssetError(Exception):
    """Raised when an asset cannot be generated or safely loaded."""


def _save_rgba(path: Path, arr: np.ndarray) -> None:
    Image.fromarray(arr.astype(np.uint8), "RGBA").save(path)


def generate_demo_assets(directory, seed: int = 7) -> dict[str, str]:
    """Create base/glow/particles PNGs of differing sizes in ``directory``.

    Returns a mapping of logical name to path relative to ``directory``.
    """
    root = Path(directory)
    root.mkdir(parents=True, exist_ok=True)

    # Base card: vertical gradient, fully opaque, 256x144.
    h, w = 144, 256
    t = np.linspace(0.0, 1.0, h, dtype=np.float64)[:, None]
    r = 40 + 60 * t
    g = 30 + 90 * t
    b = 90 + 120 * t
    base = np.zeros((h, w, 4), dtype=np.uint8)
    base[..., 0] = np.rint(np.broadcast_to(r, (h, w)))
    base[..., 1] = np.rint(np.broadcast_to(g, (h, w)))
    base[..., 2] = np.rint(np.broadcast_to(b, (h, w)))
    base[..., 3] = 255
    _save_rgba(root / "base.png", base)

    # Rarity glow: warm radial falloff with semi-transparent edge, 128x128.
    n = 128
    yy, xx = np.mgrid[0:n, 0:n].astype(np.float64)
    d = np.hypot(yy - (n - 1) / 2, xx - (n - 1) / 2) / ((n - 1) / 2)
    falloff = np.clip(1.0 - d, 0.0, 1.0) ** 2
    glow = np.zeros((n, n, 4), dtype=np.uint8)
    glow[..., 0] = 255
    glow[..., 1] = np.rint(200 + 40 * falloff)
    glow[..., 2] = np.rint(80 * (1 - falloff))
    glow[..., 3] = np.rint(230 * falloff)
    _save_rgba(root / "glow.png", glow)

    # Particle patch: sparse soft dots on transparency, 96x96.
    rng = np.random.default_rng(seed)
    m = 96
    yy, xx = np.mgrid[0:m, 0:m].astype(np.float64)
    acc = np.zeros((m, m), dtype=np.float64)
    for _ in range(24):
        cx, cy = rng.uniform(8, m - 8, 2)
        sigma = rng.uniform(1.5, 4.0)
        acc += np.exp(-((xx - cx) ** 2 + (yy - cy) ** 2) / (2 * sigma * sigma))
    acc = np.clip(acc, 0.0, 1.0)
    dots = np.zeros((m, m, 4), dtype=np.uint8)
    dots[..., 0] = 255
    dots[..., 1] = 255
    dots[..., 2] = 220
    dots[..., 3] = np.rint(255 * acc)
    _save_rgba(root / "particles.png", dots)

    return {"base": "base.png", "glow": "glow.png", "particles": "particles.png"}


def load_png(root, relpath: str) -> np.ndarray:
    """Load a PNG inside ``root`` as an RGBA uint8 array.

    Rejects paths that resolve outside ``root`` and non-PNG suffixes.
    """
    root_resolved = Path(root).resolve()
    path = (root_resolved / relpath).resolve()
    if path != root_resolved and root_resolved not in path.parents:
        raise AssetError(f"asset path {relpath!r} escapes asset directory")
    if path.suffix.lower() != ".png":
        raise AssetError(f"asset {relpath!r} is not a .png file")
    if not path.is_file():
        raise AssetError(f"asset {relpath!r} not found under {root_resolved}")
    try:
        with Image.open(path) as im:
            return np.array(im.convert("RGBA"), dtype=np.uint8)
    except Exception as exc:  # Pillow raises several error types
        raise AssetError(f"failed to decode {relpath!r}: {exc}") from exc
