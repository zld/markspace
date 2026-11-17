"""Frame-type counts and lightweight NumPy luminance statistics.

Luminance is computed from the small per-frame luma samples produced by
:mod:`frameaudit.decode`; full-resolution frames are never retained, so memory
use stays flat regardless of clip length.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np

from .repair import FrameRecord


@dataclass(slots=True)
class FrameStats:
    decoded_frames: int
    i_frames: int
    p_frames: int
    b_frames: int
    unknown_frames: int
    luma_mean: float | None
    luma_min: float | None
    luma_max: float | None
    luma_std: float | None


def compute_stats(records: list[FrameRecord]) -> FrameStats:
    """Aggregate frame-type counts and luminance statistics."""
    type_counts = {"I": 0, "P": 0, "B": 0, "?": 0}
    # Stacked samples stay tiny: len(records) x 8 x 8 float32.
    samples: list[np.ndarray] = []

    for record in records:
        pict = record.frame.pict_type
        type_counts[pict if pict in type_counts else "?"] += 1
        samples.append(record.frame.luma)

    if samples:
        grid = np.stack(samples)  # (N, 8, 8)
        luma_mean = round(float(grid.mean()), 4)
        luma_min = round(float(grid.min()), 4)
        luma_max = round(float(grid.max()), 4)
        luma_std = round(float(grid.std()), 4)
    else:
        luma_mean = luma_min = luma_max = luma_std = None

    return FrameStats(
        decoded_frames=len(records),
        i_frames=type_counts["I"],
        p_frames=type_counts["P"],
        b_frames=type_counts["B"],
        unknown_frames=type_counts["?"],
        luma_mean=luma_mean,
        luma_min=luma_min,
        luma_max=luma_max,
        luma_std=luma_std,
    )
