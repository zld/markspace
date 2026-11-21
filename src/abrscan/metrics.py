"""Encoded-size, bitrate and PSNR metrics.

Every value that will be written to the database passes through
:func:`validate_metrics`, which rejects non-finite or non-positive results.
"""

from __future__ import annotations

import math
from dataclasses import dataclass

import numpy as np

MAX_PSNR_DB = 100.0


class MetricError(ValueError):
    """Raised when a measurement is invalid or not finite."""


@dataclass(frozen=True)
class Metrics:
    encoded_bytes: int
    actual_kbps: float
    quality_db: float


def bitrate_kbps(encoded_bytes: int, frame_count: int, fps: int) -> float:
    """Actual bitrate from compressed bytes and clip duration."""
    if encoded_bytes <= 0:
        raise MetricError("encoded byte count must be positive")
    if frame_count <= 0 or fps <= 0:
        raise MetricError("frame count and fps must be positive")
    duration_s = frame_count / fps
    kbps = (encoded_bytes * 8.0) / duration_s / 1000.0
    if not math.isfinite(kbps) or kbps <= 0:
        raise MetricError("computed bitrate is not finite and positive")
    return kbps


def psnr_db(original: np.ndarray, decoded: np.ndarray) -> float:
    """PSNR in dB between two uint8 clips of identical shape."""
    if original.shape != decoded.shape:
        raise MetricError(
            f"decoded shape {decoded.shape} does not match original {original.shape}"
        )
    if original.ndim != 3:
        raise MetricError("clips must have shape (count, height, width)")
    if original.dtype != np.uint8 or decoded.dtype != np.uint8:
        raise MetricError("clips must be uint8")
    if original.size == 0:
        raise MetricError("clips must not be empty")
    if not (np.isfinite(decoded).all() if decoded.dtype.kind == "f" else True):
        raise MetricError("decoded clip contains non-finite samples")
    difference = original.astype(np.float64) - decoded.astype(np.float64)
    mse = float(np.mean(difference * difference))
    if not math.isfinite(mse) or mse < 0:
        raise MetricError("mean squared error is not finite")
    if mse == 0.0:
        raise MetricError("decoded clip is identical; PSNR is not finite")
    value = 10.0 * math.log10((255.0 * 255.0) / mse)
    if not math.isfinite(value):
        raise MetricError("PSNR is not finite")
    return value


def compute_metrics(
    original: np.ndarray, decoded: np.ndarray, encoded_bytes: int, fps: int
) -> Metrics:
    """Compute and validate all measurement values for a clip."""
    if original.ndim != 3:
        raise MetricError("original clip must have shape (count, height, width)")
    frame_count = int(original.shape[0])
    kbps = bitrate_kbps(encoded_bytes, frame_count, fps)
    quality = psnr_db(original, decoded)
    return validate_metrics(
        Metrics(encoded_bytes=encoded_bytes, actual_kbps=kbps, quality_db=quality)
    )


def validate_metrics(metrics: Metrics) -> Metrics:
    """Reject corrupt byte counts and non-finite measured values."""
    if not isinstance(metrics.encoded_bytes, int) or metrics.encoded_bytes <= 0:
        raise MetricError("encoded byte count must be a positive integer")
    if not math.isfinite(metrics.actual_kbps) or metrics.actual_kbps <= 0:
        raise MetricError("actual bitrate must be finite and positive")
    if not math.isfinite(metrics.quality_db):
        raise MetricError("quality score must be finite")
    if metrics.quality_db > MAX_PSNR_DB:
        raise MetricError("quality score exceeds the supported range")
    return metrics
