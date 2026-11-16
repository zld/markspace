"""Deterministic fixture generation.

All demo layers are drawn with Pillow and all sizes scale with the canvas,
so the pipeline is reproducible without any external assets.
"""
from __future__ import annotations

import numpy as np
from PIL import Image, ImageDraw

from .model import Clip, Keyframe, Layer, Timeline


def _gradient(width: int, height: int) -> np.ndarray:
    """Vertical two-stop gradient, built via NumPy and normalized by Pillow."""
    top = np.array([16, 24, 48, 255], dtype=np.float64)
    bottom = np.array([48, 16, 64, 255], dtype=np.float64)
    t = np.linspace(0.0, 1.0, max(height, 1), dtype=np.float64)[:, None, None]
    ramp = top + (bottom - top) * t
    arr = np.clip(np.rint(np.broadcast_to(ramp, (height, width, 4))), 0, 255)
    return np.asarray(Image.fromarray(arr.astype(np.uint8), "RGBA"))


def _rounded_rect(width: int, height: int, color: tuple[int, int, int, int]) -> np.ndarray:
    img = Image.new("RGBA", (width, height), (0, 0, 0, 0))
    draw = ImageDraw.Draw(img)
    draw.rounded_rectangle(
        [0, 0, width - 1, height - 1], radius=max(2, min(width, height) // 5), fill=color
    )
    return np.asarray(img)


def _disc(diameter: int, color: tuple[int, int, int, int]) -> np.ndarray:
    img = Image.new("RGBA", (diameter, diameter), (0, 0, 0, 0))
    draw = ImageDraw.Draw(img)
    draw.ellipse([0, 0, diameter - 1, diameter - 1], fill=color)
    return np.asarray(img)


def build_demo_timeline(
    fps: int, frame_count: int, width: int, height: int
) -> Timeline:
    """Build the deterministic demo timeline for the given format."""
    last = max(frame_count - 1, 0)
    half = frame_count // 2

    bg = Clip(
        name="background",
        start_frame=0,
        end_frame=frame_count,
        blend_mode="source-over",
        keyframes=(Keyframe(0, 1.0, 0.0, 0.0),),
        image=_gradient(width, height),
    )

    rect_w = max(4, width // 4)
    rect_h = max(4, height // 3)
    mid = Clip(
        name="mid-rect",
        start_frame=0,
        end_frame=frame_count,
        blend_mode="multiply",
        keyframes=(
            Keyframe(0, 0.25, -rect_w / 2.0, height / 6.0),
            Keyframe(last, 0.9, width - rect_w / 2.0, height / 2.0),
        ),
        image=_rounded_rect(rect_w, rect_h, (200, 80, 40, 200)),
    )

    diameter = max(4, min(width, height) // 3)
    top = Clip(
        name="top-disc",
        start_frame=half,
        end_frame=frame_count,
        blend_mode="screen",
        keyframes=(
            Keyframe(half, 0.8, width - diameter / 2.0, 4.0),
            Keyframe(last, 0.4, width / 8.0, height - diameter / 2.0),
        ),
        image=_disc(diameter, (60, 200, 255, 160)),
    )

    # Empty clip (zero-length interval): must be accepted and skipped.
    empty = Clip(
        name="empty",
        start_frame=half,
        end_frame=half,
        blend_mode="source-over",
        keyframes=(),
        image=_disc(4, (255, 255, 255, 255)),
    )

    return Timeline(
        fps=fps,
        frame_count=frame_count,
        width=width,
        height=height,
        layers=[
            Layer("base", [bg]),
            Layer("mid", [mid]),
            Layer("top", [empty, top]),
        ],
    )
