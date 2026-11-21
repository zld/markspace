"""Deterministic synthetic sample clips.

Frames are generated from closed-form functions instead of a random number
generator so that repeated runs on the same platform produce byte-identical
input.  The SHA-256 of the canonical frame bytes plus geometry is the content
hash used as part of every task key.
"""

from __future__ import annotations

import hashlib
from dataclasses import dataclass

import numpy as np

from .params import FIXTURE_NAMES, ParamError

DEFAULT_WIDTH = 96
DEFAULT_HEIGHT = 64
DEFAULT_FRAMES = 48
DEFAULT_FPS = 24
_HASH_SEED = {
    "moving-texture": 0x5EED_1001,
    "pan-texture": 0x5EED_2002,
    "static-texture": 0x5EED_3003,
}


@dataclass(frozen=True)
class Fixture:
    """A generated grayscale clip and its identity."""

    name: str
    width: int
    height: int
    fps: int
    frames: np.ndarray  # (frame_count, height, width), uint8
    content_hash: str

    @property
    def frame_count(self) -> int:
        return int(self.frames.shape[0])

    @property
    def duration_s(self) -> float:
        return self.frame_count / self.fps


def _coordinate_noise(width: int, height: int, seed: int) -> np.ndarray:
    """Deterministic integer-hash noise in [0, 1) at pixel resolution."""
    xs = np.arange(width, dtype=np.uint64)[None, :]
    ys = np.arange(height, dtype=np.uint64)[:, None]
    value = xs * np.uint64(374_761_393) + ys * np.uint64(668_265_263)
    value = value + np.uint64(seed)
    value = (value ^ (value >> np.uint64(13))) * np.uint64(1_274_126_177)
    value = value ^ (value >> np.uint64(16))
    return (value & np.uint64(0xFFFF)).astype(np.float32) / 65536.0


def _base_texture(width: int, height: int, seed: int) -> np.ndarray:
    """Smooth gradients, fine grain and diagonal ridges in 0..255."""
    ys, xs = np.mgrid[0:height, 0:width].astype(np.float32)
    grain = _coordinate_noise(width, height, seed)
    ripple = np.sin(xs * 0.21 + ys * 0.13) * 34.0
    checker = np.where(((xs // 6) + (ys // 5)) % 2 == 0, 22.0, -22.0)
    diagonal = ((xs + ys) % 17) * 1.7
    texture = 118.0 + ripple + checker + diagonal + (grain - 0.5) * 46.0
    return np.clip(texture, 0.0, 255.0)


def _add_moving_object(
    frame: np.ndarray, frame_index: int, total: int, width: int, height: int
) -> np.ndarray:
    """Paint a soft blob travelling on an ellipse."""
    angle = 2.0 * np.pi * frame_index / max(total, 1)
    cx = 0.5 * width + 0.30 * width * np.cos(angle)
    cy = 0.5 * height + 0.30 * height * np.sin(angle * 1.25)
    radius = max(4.0, min(width, height) * 0.09)
    ys, xs = np.mgrid[0:height, 0:width].astype(np.float32)
    distance2 = (xs - cx) ** 2 + (ys - cy) ** 2
    blob = np.exp(-distance2 / (2.0 * radius * radius)) * 85.0
    return np.clip(frame.astype(np.float32) + blob, 0.0, 255.0)


def _pan_texture(
    texture: np.ndarray, frame_index: int, total: int, moving_object: bool
) -> np.ndarray:
    height, width = texture.shape
    span = max(total - 1, 1)
    dx = int(round((frame_index / span) * (width * 0.18)))
    dy = int(round((frame_index / span) * (height * 0.10)))
    shifted = np.roll(np.roll(texture, dx, axis=1), dy, axis=0)
    if moving_object:
        shifted = _add_moving_object(shifted, frame_index, total, width, height)
    return shifted.astype(np.float32)


def render_fixture(
    name: str = "moving-texture",
    width: int = DEFAULT_WIDTH,
    height: int = DEFAULT_HEIGHT,
    frame_count: int = DEFAULT_FRAMES,
    fps: int = DEFAULT_FPS,
) -> Fixture:
    """Render one of the built-in clips deterministically."""
    if name not in FIXTURE_NAMES:
        raise ParamError(f"unknown fixture {name!r}; choose from {FIXTURE_NAMES}")
    if not (1 <= frame_count <= 240):
        raise ParamError("frame count is out of range")
    if not (8 <= width <= 320 and 8 <= height <= 240):
        raise ParamError("fixture geometry is out of range")
    if not (1 <= fps <= 120):
        raise ParamError("fps is out of range")

    texture = _base_texture(width, height, _HASH_SEED[name])
    frames = np.empty((frame_count, height, width), dtype=np.uint8)
    for index in range(frame_count):
        if name == "static-texture":
            frame = texture.copy()
        elif name == "pan-texture":
            frame = _pan_texture(texture, index, frame_count, moving_object=False)
        else:  # moving-texture
            frame = _pan_texture(texture, index, frame_count, moving_object=True)
        frames[index] = np.clip(np.rint(frame), 0, 255).astype(np.uint8)

    digest = hashlib.sha256()
    digest.update(
        f"abrscan-fixture-v1|{name}|{width}|{height}|{frame_count}|{fps}|".encode()
    )
    digest.update(np.ascontiguousarray(frames).tobytes())
    return Fixture(
        name=name,
        width=width,
        height=height,
        fps=fps,
        frames=frames,
        content_hash=digest.hexdigest(),
    )
