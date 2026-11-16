"""Frame alignment and PSNR quality scoring with NumPy.

Both clips are decoded to raw yuv420p through an FFmpeg pipe; the source
is scaled to the candidate's resolution first. Frames are paired in
lockstep and compared on the luma plane. If the frame counts cannot be
aligned within a half-frame tolerance the candidate fails.
"""

from __future__ import annotations

import math
import subprocess
from pathlib import Path
from typing import Iterator

import numpy as np

MAX_ALIGN_ERROR_FRAMES = 0.5


class AlignError(Exception):
    """Frames or timestamps cannot be aligned within half a frame."""


def _decode_luma(
    path: str | Path, width: int, height: int, scale: bool
) -> Iterator[np.ndarray]:
    """Yield luma planes (float64 HxW) decoded via an FFmpeg rawvideo pipe."""
    argv = ["ffmpeg", "-v", "error", "-i", str(path)]
    if scale:
        argv += ["-vf", f"scale={width}:{height}"]
    argv += ["-f", "rawvideo", "-pix_fmt", "yuv420p", "-"]
    frame_bytes = width * height * 3 // 2
    luma_bytes = width * height
    proc = subprocess.Popen(
        argv, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL
    )
    assert proc.stdout is not None
    try:
        while True:
            buf = proc.stdout.read(frame_bytes)
            if len(buf) < frame_bytes:
                break
            plane = np.frombuffer(buf[:luma_bytes], dtype=np.uint8)
            yield plane.reshape(height, width).astype(np.float64)
    finally:
        proc.stdout.close()
        proc.wait()


def quality_score(
    src_path: str | Path, enc_path: str | Path, width: int, height: int
) -> tuple[float, float, int]:
    """Return (score 0..100, PSNR dB, aligned frame count).

    score = clamp((PSNR - 20) * 2.5, 0, 100) with
    PSNR = 10 * log10(255^2 / MSE) over aligned luma samples.
    """
    src_frames = _decode_luma(src_path, width, height, scale=True)
    enc_frames = _decode_luma(enc_path, width, height, scale=False)
    n_src = n_enc = 0
    sse = 0.0
    while True:
        ref = next(src_frames, None)
        if ref is not None:
            n_src += 1
        cand = next(enc_frames, None)
        if cand is not None:
            n_enc += 1
        if ref is None or cand is None:
            break
        diff = ref - cand
        sse += float(np.dot(diff.ravel(), diff.ravel()))
    if n_src == 0:
        raise AlignError("no decodable frames to align")
    if abs(n_src - n_enc) > MAX_ALIGN_ERROR_FRAMES:
        raise AlignError(
            f"frame count mismatch beyond half-frame tolerance: "
            f"source={n_src} encoded={n_enc}"
        )
    mse = sse / (n_src * width * height)
    psnr = math.inf if mse == 0.0 else 10.0 * math.log10(255.0 * 255.0 / mse)
    score = min(100.0, max(0.0, (psnr - 20.0) * 2.5))
    return score, psnr, n_src
