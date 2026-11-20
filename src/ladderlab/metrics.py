"""Quality and bitrate metrics for ladderlab research streams.

PSNR is computed per clip against the original frames; the per-task
quality score is the WORST clip score so a single weak segment cannot
hide behind strong ones. Zero error maps to a finite cap (99 dB) so
scores stay comparable and JSON-serializable.
"""
from __future__ import annotations

import numpy as np

from .codec import Params, decode_clip, encode_clip, stream_hash
from .fixture import Clip

PSNR_CAP_DB = 99.0
MAX_ABS = 255.0


class MetricError(Exception):
    """Raised when a clip cannot be scored; message states the reason."""


def psnr_db(orig: np.ndarray, recon: np.ndarray) -> float:
    """Peak signal-to-noise ratio in dB; finite even for zero error."""
    if orig.shape != recon.shape:
        raise MetricError(
            f"shape mismatch: original {orig.shape} vs reconstructed {recon.shape}"
        )
    if orig.shape[0] < 1:
        raise MetricError("frame count mismatch: clip has zero frames")
    err = orig.astype(np.float64) - recon.astype(np.float64)
    mse = float(np.mean(err * err))
    if mse == 0.0:
        return PSNR_CAP_DB
    return 10.0 * np.log10(MAX_ABS * MAX_ABS / mse)


def evaluate_params(clips: list[Clip], params: Params) -> dict:
    """Encode every clip with ``params`` and score the result.

    Returns actual_bps from the real serialized byte count, the worst
    per-clip PSNR, and a content hash of the concatenated stream.
    Raises MetricError with an explicit reason on shape/frame problems.
    """
    total_bytes = 0
    total_s = 0.0
    per_clip: dict[str, float] = {}
    hashes: list[str] = []
    for clip in clips:
        blob = encode_clip(clip.frames, params)
        recon = decode_clip(blob)
        if recon.shape != clip.frames.shape:
            raise MetricError(
                f"clip {clip.name}: decoded shape {recon.shape} "
                f"!= source {clip.frames.shape}"
            )
        per_clip[clip.name] = round(psnr_db(clip.frames, recon), 4)
        total_bytes += len(blob)
        total_s += clip.duration_s
        hashes.append(stream_hash(blob))
    if total_s <= 0:
        raise MetricError("total clip duration is zero")
    return {
        "param_key": params.key,
        "actual_bps": int(round(total_bytes * 8 / total_s)),
        "nbytes": total_bytes,
        "quality_score": round(min(per_clip.values()), 4),
        "per_clip": per_clip,
        "stream_hash": hashes[0] if len(hashes) == 1 else _combined_hash(hashes),
    }


def _combined_hash(hashes: list[str]) -> str:
    import hashlib

    h = hashlib.sha256()
    for item in hashes:
        h.update(item.encode())
    return h.hexdigest()
