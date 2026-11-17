"""Adaptive threshold estimation from a frame-level RMS series.

Noise floor is estimated with a low quantile; two factors produce a dual
(hysteresis) threshold pair.  Because the quantile alone cannot separate
speech from floor in single-mode recordings (a constant tone or a pure noise
floor), the relative inter-quartile spread of the RMS distribution decides
between a bimodal speech/silence model and a single-level model.
"""

from __future__ import annotations

import numpy as np

# Single-mode RMS distributions (constant tone, pure floor) have an IQR below
# this fraction of their median; bimodal speech/silence material exceeds it.
SINGLE_MODE_REL_SPREAD = 0.1
# Levels below this fraction of full scale are treated as a noise floor.
FLOOR_LEVEL_LIMIT = 0.01


def estimate(
    rms: np.ndarray,
    noise_quantile: float,
    voice_factor: float,
    silence_factor: float,
) -> tuple[float, float, float, float]:
    """Return ``(noise_floor, low_threshold, high_threshold, rms_max)``."""

    noise = float(np.quantile(rms, noise_quantile))
    if noise <= 0.0:
        # Degenerate floors (many zero-RMS frames) collapse thresholds to
        # zero; fall back to the median, then the mean, of observed RMS.
        noise = float(np.median(rms)) or float(np.mean(rms))

    q75 = float(np.quantile(rms, 0.75))
    q25 = float(np.quantile(rms, 0.25))
    median = float(np.median(rms))
    spread = q75 - q25
    rms_max = float(rms.max())
    relative_spread = spread / median if median > 0.0 else 0.0

    if relative_spread < SINGLE_MODE_REL_SPREAD:
        # Single-mode material: the quantile cannot distinguish floor from
        # voice, so decide from the absolute level against full scale.
        level = max(median, rms_max)
        if level <= FLOOR_LEVEL_LIMIT:
            # Pure noise floor: thresholds sit well above every frame.
            t_hi = level * 10.0 + 1.0
            t_lo = level * 5.0 + 0.5
        else:
            # Constant voiced region: thresholds in the gap below the level.
            t_hi = 0.5 * level
            t_lo = 0.25 * level
        return noise, t_lo, t_hi, rms_max

    # Bimodal case: factor-based thresholds, lifted above floor jitter and
    # capped inside the RMS spread so a narrow dynamic range still works.
    t_hi = voice_factor * noise
    t_lo = silence_factor * noise
    t_hi = max(1.2 * noise, min(t_hi, q25 + 0.5 * spread))
    t_lo = max(1.1 * noise, min(t_lo, 0.5 * t_hi))
    return noise, t_lo, t_hi, rms_max
