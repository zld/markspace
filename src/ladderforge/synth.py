"""Deterministic synthetic footage generation.

Clips are generated with NumPy as raw 8-bit yuv420p so FFmpeg can consume them
without a demuxer.  The scene is a moving texture that changes every frame; the
same seed and geometry always produce byte-identical output, which is what
makes source hashes (and therefore task keys) stable across runs.
"""

from __future__ import annotations

import hashlib
from dataclasses import dataclass
from pathlib import Path

import numpy as np

from .plan import Resolution


@dataclass(frozen=True)
class SourceClip:
    resolution: Resolution
    frames: int
    fps: int
    seed: int
    path: Path
    sha256: str

    @property
    def frame_bytes(self) -> int:
        return self.resolution.width * self.resolution.height * 3 // 2

    @property
    def expected_size(self) -> int:
        return self.frame_bytes * self.frames


def render_frame(resolution: Resolution, index: int, seed: int) -> np.ndarray:
    """Return one full yuv420p frame (Y, then U, then V planes)."""
    width, height = resolution.width, resolution.height
    rng = np.random.default_rng(seed * 1_000_003 + index)

    xx = np.arange(width, dtype=np.float64) / width
    yy = np.arange(height, dtype=np.float64) / height
    gx, gy = np.meshgrid(xx, yy)

    phase = 2.0 * np.pi * (3.0 * gx + 2.0 * gy + 0.15 * index)
    waves = 0.5 + 0.5 * np.sin(phase)
    checker = (np.floor(gx * 8.0 + 0.2 * index) + np.floor(gy * 8.0)) % 2.0
    grain = rng.integers(0, 24, size=(height, width), dtype=np.int32)

    luma = 16.0 + 190.0 * (0.62 * waves + 0.18 * checker) + grain
    luma = np.clip(luma, 16, 235).astype(np.uint8)

    u_phase = 2.0 * np.pi * (1.5 * gy - 0.08 * index)
    v_phase = 2.0 * np.pi * (1.5 * gx + 0.12 * index)
    chroma_u = np.clip(128.0 + 36.0 * np.sin(u_phase), 16, 240).astype(np.uint8)
    chroma_v = np.clip(128.0 + 36.0 * np.cos(v_phase), 16, 240).astype(np.uint8)
    chroma_u = chroma_u[::2, ::2]
    chroma_v = chroma_v[::2, ::2]

    return np.concatenate(
        (luma.ravel(), chroma_u.ravel(), chroma_v.ravel())
    )


def source_filename(resolution: Resolution, frames: int, fps: int, seed: int) -> str:
    return f"source_{resolution.label}_{frames}f_{fps}fps_seed{seed}.yuv"


def generate_clip(
    resolution: Resolution,
    frames: int,
    fps: int,
    seed: int,
    dest_dir: Path,
) -> SourceClip:
    """Generate (or reuse) a deterministic raw clip and hash its bytes."""
    dest_dir.mkdir(parents=True, exist_ok=True)
    path = dest_dir / source_filename(resolution, frames, fps, seed)
    expected = resolution.width * resolution.height * 3 // 2 * frames

    digest = hashlib.sha256()
    if path.exists() and path.stat().st_size == expected:
        with path.open("rb") as handle:
            for chunk in iter(lambda: handle.read(1 << 20), b""):
                digest.update(chunk)
        return SourceClip(resolution, frames, fps, seed, path, digest.hexdigest())

    temporary = path.with_suffix(path.suffix + ".partial")
    with temporary.open("wb") as handle:
        for index in range(frames):
            payload = render_frame(resolution, index, seed).tobytes()
            handle.write(payload)
            digest.update(payload)
    temporary.replace(path)
    size = path.stat().st_size
    if size != expected:
        raise RuntimeError(
            f"generated source {path} has {size} bytes, expected {expected}"
        )
    return SourceClip(resolution, frames, fps, seed, path, digest.hexdigest())


def read_reference_luma(clip: SourceClip) -> np.ndarray:
    """Return a (frames, H, W) uint8 view of the reference Y plane."""
    data = clip.path.read_bytes()
    if len(data) != clip.expected_size:
        raise RuntimeError(f"reference clip {clip.path} has an unexpected size")
    width, height = clip.resolution.width, clip.resolution.height
    y_size = width * height
    frame_size = clip.frame_bytes
    planes = [
        np.frombuffer(data, dtype=np.uint8, count=y_size, offset=i * frame_size)
        for i in range(clip.frames)
    ]
    return np.stack(planes).reshape(clip.frames, height, width)
