"""Deterministic sample-clip generation for ladderlab.

Three synthetic 128x72 @ 24fps clips are produced from a single seed:
  - "flat":   smooth gradient with faint dither (static picture)
  - "motion": translating plaid texture with sensor noise (camera motion)
  - "cut":    two distinct block scenes with a hard cut and particles

Same seed + spec -> identical frames, so the input fingerprint is stable.
"""
from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass

import numpy as np

WIDTH = 128
HEIGHT = 72
FPS = 24
FRAMES_PER_CLIP = 36  # 1.5 s per clip, 4.5 s of material in total
CLIP_NAMES = ("flat", "motion", "cut")
FIXTURE_VERSION = 1

# Noise levels (stddev) chosen so the 36-combo grid spans roughly
# 1e5..1e7 bps once serialized; see codec.py.
_FLAT_NOISE = 1.0
_MOTION_NOISE = 9.0
_CUT_NOISE = 4.0


@dataclass(frozen=True)
class Clip:
    name: str
    frames: np.ndarray  # uint8, shape (T, HEIGHT, WIDTH, 3)

    @property
    def duration_s(self) -> float:
        return self.frames.shape[0] / FPS


def _to_uint8(a: np.ndarray) -> np.ndarray:
    return np.clip(np.rint(a), 0, 255).astype(np.uint8)


def _flat_clip(rng: np.random.Generator) -> np.ndarray:
    t = FRAMES_PER_CLIP
    yy = np.linspace(40, 180, HEIGHT, dtype=np.float64)[:, None]
    xx = np.linspace(0, 30, WIDTH, dtype=np.float64)[None, :]
    base = yy + xx  # smooth diagonal gradient
    frames = np.empty((t, HEIGHT, WIDTH, 3), dtype=np.uint8)
    for i in range(t):
        noise = rng.normal(0.0, _FLAT_NOISE, size=(HEIGHT, WIDTH, 3))
        frame = np.stack([base + c * 7.0 for c in range(3)], axis=-1) + noise
        frames[i] = _to_uint8(frame)
    return frames


def _motion_clip(rng: np.random.Generator) -> np.ndarray:
    t = FRAMES_PER_CLIP
    yy, xx = np.mgrid[0:HEIGHT, 0:WIDTH].astype(np.float64)
    frames = np.empty((t, HEIGHT, WIDTH, 3), dtype=np.uint8)
    phase = rng.uniform(0, 2 * np.pi, size=3)
    for i in range(t):
        dx, dy = 1.7 * i, 0.6 * i  # steady camera pan
        plaid = (
            np.sin((xx + dx) * 0.55 + phase[0])
            + np.sin((yy + dy) * 0.83 + phase[1])
            + np.sin((xx + dx + yy + dy) * 0.21 + phase[2])
        )
        base = 128.0 + 55.0 * plaid
        noise = rng.normal(0.0, _MOTION_NOISE, size=(HEIGHT, WIDTH, 3))
        frame = base[..., None] + noise
        frames[i] = _to_uint8(frame)
    return frames


def _cut_clip(rng: np.random.Generator) -> np.ndarray:
    t = FRAMES_PER_CLIP
    frames = np.empty((t, HEIGHT, WIDTH, 3), dtype=np.uint8)
    block = 8
    scene_a = rng.integers(30, 225, size=(HEIGHT // block, WIDTH // block, 3))
    scene_b = rng.integers(30, 225, size=(HEIGHT // block, WIDTH // block, 3))
    scene_a = np.kron(scene_a, np.ones((block, block, 1))).astype(np.float64)
    scene_b = np.kron(scene_b, np.ones((block, block, 1))).astype(np.float64)
    # Particles: deterministic pseudo-random dots drifting across both scenes.
    n_dots = 24
    dot_xy = rng.integers(0, [WIDTH, HEIGHT], size=(n_dots, 2))
    dot_v = rng.uniform(-2.0, 2.0, size=(n_dots, 2))
    cut_at = t // 2
    for i in range(t):
        scene = scene_a if i < cut_at else scene_b
        frame = scene + rng.normal(0.0, _CUT_NOISE, size=(HEIGHT, WIDTH, 3))
        for d in range(n_dots):
            x = int((dot_xy[d, 0] + dot_v[d, 0] * i) % WIDTH)
            y = int((dot_xy[d, 1] + dot_v[d, 1] * i) % HEIGHT)
            frame[y, x] = 255.0
        frames[i] = _to_uint8(frame)
    return frames


def generate_clips(seed: int) -> list[Clip]:
    """Build the three deterministic clips for ``seed``."""
    rng = np.random.default_rng(seed)
    return [
        Clip("flat", _flat_clip(rng)),
        Clip("motion", _motion_clip(rng)),
        Clip("cut", _cut_clip(rng)),
    ]


def spec_dict(seed: int) -> dict:
    return {
        "fixture_version": FIXTURE_VERSION,
        "seed": seed,
        "width": WIDTH,
        "height": HEIGHT,
        "fps": FPS,
        "frames_per_clip": FRAMES_PER_CLIP,
        "clips": list(CLIP_NAMES),
    }


def input_fingerprint(clips: list[Clip], seed: int) -> str:
    """Stable fingerprint over spec + actual pixel data."""
    h = hashlib.sha256()
    h.update(json.dumps(spec_dict(seed), sort_keys=True).encode())
    for clip in clips:
        h.update(clip.name.encode())
        h.update(clip.frames.tobytes())
    return h.hexdigest()
