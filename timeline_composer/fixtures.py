"""Deterministic sample fixtures for demos and tests.

Generates a small set of PNG assets *inside a caller-provided directory* and a
timeline document that exercises every blend mode, independent opacity /
position / scale tracks, half-open layer windows, off-canvas geometry, a
fully transparent layer, and three audio cues. Nothing here uses randomness;
the same inputs always yield byte-identical PNGs and the same timeline.
"""

from __future__ import annotations

from fractions import Fraction
from pathlib import Path

import numpy as np
from PIL import Image

from .schema import AudioEvent, SchemaError, parse_timeline

DEMO_LAYER_ASSETS: tuple[tuple[str, tuple[int, int]], ...] = (
    ("backdrop.png", (480, 270)),
    ("character.png", (96, 160)),
    ("glow.png", (128, 128)),
    ("sparkle.png", (64, 64)),
    ("blank.png", (32, 32)),
)

# Cue timings are chosen so the worst-case error is a realistic ~6.01 ms:
# frame 6 starts at 0.2002 s while the "impact" cue sits at 206.208 ms.
DEMO_AUDIO_EVENTS: tuple[dict[str, object], ...] = (
    {"label": "impact", "sample": 9_898, "sample_rate": 48_000},
    {"label": "whoosh", "sample": 14_414, "sample_rate": 48_000},
    {"label": "chime", "sample": 105_706, "sample_rate": 48_000},
)


def _vertical_gradient(width: int, height: int) -> np.ndarray:
    rows = np.linspace(0.05, 0.55, height, dtype=np.float32)
    columns = np.linspace(0.0, 0.25, width, dtype=np.float32)
    r = np.clip(rows[:, None] + columns[None, :] * 0.4, 0.0, 1.0)
    g = np.clip(rows[:, None] * 0.8 + columns[None, :], 0.0, 1.0)
    b = np.clip(0.55 - rows[:, None] * 0.4 + columns[None, :] * 0.2, 0.0, 1.0)
    rgb = np.stack([r, g, b], axis=2)
    alpha = np.ones((height, width), dtype=np.float32)
    return _to_rgba8(rgb, alpha)


def _character(width: int, height: int) -> np.ndarray:
    yy, xx = np.mgrid[0:height, 0:width]
    cx, cy = (width - 1) / 2.0, (height - 1) / 2.0
    radius = np.sqrt(((xx - cx) / (width * 0.42)) ** 2 + ((yy - cy) / (height * 0.48)) ** 2)
    body = np.clip(1.0 - radius, 0.0, 1.0)
    rgb = np.stack(
        [
            np.full_like(body, 0.92),
            np.clip(0.35 + body * 0.4, 0.0, 1.0),
            np.clip(0.25 + body * 0.2, 0.0, 1.0),
        ],
        axis=2,
    )
    return _to_rgba8(rgb, body)


def _glow(width: int, height: int) -> np.ndarray:
    yy, xx = np.mgrid[0:height, 0:width]
    cx, cy = (width - 1) / 2.0, (height - 1) / 2.0
    distance = np.sqrt((xx - cx) ** 2 + (yy - cy) ** 2) / (min(width, height) * 0.5)
    falloff = np.clip(1.0 - distance, 0.0, 1.0) ** 2
    rgb = np.stack(
        [np.full_like(falloff, 1.0), np.clip(falloff, 0.0, 1.0), np.full_like(falloff, 0.3)],
        axis=2,
    )
    return _to_rgba8(rgb, falloff * 0.85)


def _sparkle(width: int, height: int) -> np.ndarray:
    yy, xx = np.mgrid[0:height, 0:width]
    cx, cy = (width - 1) / 2.0, (height - 1) / 2.0
    arm_x = np.exp(-(((yy - cy) / 3.0) ** 2)) * np.exp(-((xx - cx) / (width * 0.45)) ** 2)
    arm_y = np.exp(-(((xx - cx) / 3.0) ** 2)) * np.exp(-((yy - cy) / (height * 0.45)) ** 2)
    intensity = np.clip(np.maximum(arm_x, arm_y), 0.0, 1.0)
    rgb = np.full((height, width, 3), 1.0, dtype=np.float32)
    return _to_rgba8(rgb, intensity)


def _to_rgba8(rgb: np.ndarray, alpha: np.ndarray) -> np.ndarray:
    out = np.empty((*alpha.shape, 4), dtype=np.uint8)
    out[..., :3] = np.floor(np.clip(rgb, 0.0, 1.0) * 255.0 + 0.5).astype(np.uint8)
    out[..., 3] = np.floor(np.clip(alpha, 0.0, 1.0) * 255.0 + 0.5).astype(np.uint8)
    return out


_BUILDERS = {
    "backdrop.png": _vertical_gradient,
    "character.png": _character,
    "glow.png": _glow,
    "sparkle.png": _sparkle,
    "blank.png": lambda width, height: np.zeros((height, width, 4), dtype=np.uint8),
}


def write_demo_assets(directory: str | Path) -> Path:
    """Write the deterministic demo PNGs into ``directory`` and return it."""
    root = Path(directory)
    root.mkdir(parents=True, exist_ok=True)
    for name, (width, height) in DEMO_LAYER_ASSETS:
        pixels = _BUILDERS[name](width, height)
        target = root / name
        Image.fromarray(pixels, mode="RGBA").save(target, format="PNG", optimize=False)
    return root


def demo_timeline_document(
    frames: int, fps: str, width: int, height: int
) -> dict[str, object]:
    """Return the demo timeline document for the given grid parameters."""
    try:
        rate = Fraction(fps)
    except (ValueError, ZeroDivisionError) as exc:
        raise SchemaError(f"fps is not a valid rational: {fps!r}") from exc
    if rate <= 0:
        raise SchemaError(f"fps must be positive, got {fps!r}")
    duration = Fraction(frames, 1) / rate
    scale_x = Fraction(width, 320)
    scale_y = Fraction(height, 180)
    return {
        "canvas": {"width": width, "height": height},
        "fps": fps,
        "frames": frames,
        "layers": [
            {
                "id": "backdrop",
                "source": "backdrop.png",
                "order": 0,
                "blend": "normal",
                "position": {"x": 0, "y": 0},
                "scale": {"x": scale_x, "y": scale_y},
                "opacity": {"keyframes": [
                    {"time": 0, "value": "1/2"},
                    {"time": duration, "value": 1},
                ]},
            },
            {
                "id": "character",
                "source": "character.png",
                "order": 10,
                "blend": "normal",
                "position": {"keyframes": [
                    {"time": 0, "x": -24, "y": 20},
                    {"time": duration, "x": "1/2", "y": 12},
                ]},
                "scale": {"keyframes": [
                    {"time": 0, "value": "1/2"},
                    {"time": duration, "value": "3/4"},
                ]},
                "opacity": 1,
            },
            {
                "id": "glow",
                "source": "glow.png",
                "order": 20,
                "blend": "screen",
                "position": {"x": 8, "y": 8},
                "scale": {"value": 1},
                "opacity": {"keyframes": [
                    {"time": 0, "value": "1/4"},
                    {"time": duration, "value": "3/4"},
                ]},
            },
            {
                "id": "sparkle",
                "source": "sparkle.png",
                "order": 20,
                "blend": "multiply",
                "position": {"keyframes": [
                    {"time": 0, "x": 200, "y": 0},
                    {"time": duration, "x": "2/3", "y": "1/3"},
                ]},
                "scale": {"value": 1},
                "opacity": "3/4",
            },
            {
                "id": "ghost",
                "source": "blank.png",
                "order": 30,
                "blend": "normal",
                "start": 0,
                "end": "1/1000",
                "opacity": 0,
            },
        ],
        "audio_events": [dict(event) for event in DEMO_AUDIO_EVENTS],
    }


def build_demo(
    directory: str | Path, frames: int, fps: str, width: int, height: int
):
    """Write demo assets and return ``(timeline, asset_root)``."""
    root = write_demo_assets(directory)
    timeline = parse_timeline(demo_timeline_document(frames, fps, width, height))
    return timeline, root


__all__ = [
    "DEMO_AUDIO_EVENTS",
    "DEMO_LAYER_ASSETS",
    "build_demo",
    "demo_timeline_document",
    "write_demo_assets",
]
