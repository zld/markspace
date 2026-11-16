"""Deterministic Y4M sample clip generation.

The clip mixes content that stresses encoders differently: a smooth dark
gradient (banding risk), a fast-moving sharp-edged block (motion), and a
small high-contrast HUD strip (fine detail). Output is deterministic for a
given seed so scans are reproducible.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

import numpy as np

MAX_WIDTH = 1920
MAX_HEIGHT = 1080
MAX_DURATION_S = 60.0


@dataclass(frozen=True)
class ClipSpec:
    width: int = 640
    height: int = 360
    fps: int = 30
    duration_s: float = 2.0
    seed: int = 0

    @property
    def frames(self) -> int:
        return max(1, int(round(self.fps * self.duration_s)))


def validate_spec(spec: ClipSpec) -> None:
    """Enforce yuv420p layout and the 1080p / 60s clip limits."""
    if spec.width % 2 or spec.height % 2:
        raise ValueError("width and height must be even for yuv420p")
    if not (16 <= spec.width <= MAX_WIDTH):
        raise ValueError(f"width out of range 16..{MAX_WIDTH}: {spec.width}")
    if not (16 <= spec.height <= MAX_HEIGHT):
        raise ValueError(f"height out of range 16..{MAX_HEIGHT}: {spec.height}")
    if not (1 <= spec.fps <= 120):
        raise ValueError(f"fps out of range 1..120: {spec.fps}")
    if not (0.1 <= spec.duration_s <= MAX_DURATION_S):
        raise ValueError(f"duration out of range 0.1..{MAX_DURATION_S}s")


def _luma_frame(spec: ClipSpec, idx: int, rng: np.random.Generator) -> np.ndarray:
    w, h = spec.width, spec.height
    # Dark vertical gradient (16..60) stresses low-light banding.
    grad = np.linspace(16.0, 60.0, h, dtype=np.float32)[:, None]
    grad = np.broadcast_to(grad, (h, w)).copy()
    t = idx / max(1, spec.frames - 1)
    # Moving bright block with sharp edges traverses the frame.
    bw, bh = max(8, w // 8), max(8, h // 6)
    bx = int((w - bw) * t)
    by = int((h - bh) * (0.5 + 0.4 * np.sin(2.0 * np.pi * t)))
    grad[by : by + bh, bx : bx + bw] = 220.0
    # Small HUD-like strip: alternating hard-contrast 4px bars, top-left.
    bar = max(2, h // 90)
    for row in range(3):
        y0 = 8 + row * bar
        grad[y0 : y0 + bar, 8 : 8 + max(16, w // 10)] = 235.0 if row % 2 == 0 else 20.0
    noise = rng.normal(0.0, 1.5, size=(h, w))
    return np.clip(grad + noise, 0, 255).astype(np.uint8)


def _chroma_frames(spec: ClipSpec, idx: int) -> tuple[np.ndarray, np.ndarray]:
    cw, ch = spec.width // 2, spec.height // 2
    drift = int(12.0 * np.sin(2.0 * np.pi * idx / max(1, spec.frames)))
    u = np.full((ch, cw), 128 + drift, dtype=np.uint8)
    v = np.full((ch, cw), 128 - drift, dtype=np.uint8)
    return u, v


def write_y4m(path: str | Path, spec: ClipSpec) -> int:
    """Write the clip as Y4M (C420jpeg). Returns the frame count."""
    validate_spec(spec)
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    rng = np.random.default_rng(spec.seed)
    header = (
        f"YUV4MPEG2 W{spec.width} H{spec.height} "
        f"F{spec.fps}:1 Ip A1:1 C420jpeg\n"
    )
    with open(path, "wb") as fh:
        fh.write(header.encode("ascii"))
        for i in range(spec.frames):
            fh.write(b"FRAME\n")
            fh.write(_luma_frame(spec, i, rng).tobytes())
            u, v = _chroma_frames(spec, i)
            fh.write(u.tobytes())
            fh.write(v.tobytes())
    return spec.frames
