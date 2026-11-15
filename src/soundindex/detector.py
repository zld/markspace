"""Hysteresis-gate clip detector with duration constraints and fade guard."""
from __future__ import annotations

import numpy as np

from .features import noise_floor_db, smooth_db
from .models import DetectionResult, DetectParams, Interval

_NOISE_WINDOW_S = 2.0


def _active_mask(env_db: np.ndarray, floor_db: np.ndarray,
                 margin_db: float, hysteresis_db: float) -> np.ndarray:
    """Hysteresis gate: opens at floor+margin, closes hysteresis_db lower."""
    high = floor_db + margin_db
    low = high - hysteresis_db
    active = np.zeros(env_db.size, dtype=bool)
    state = False
    for i in range(env_db.size):
        e = env_db[i]
        state = e >= low[i] if state else e >= high[i]
        active[i] = state
    return active


def _runs(mask: np.ndarray):
    runs = []
    start = None
    for i, v in enumerate(mask):
        if v and start is None:
            start = i
        elif not v and start is not None:
            runs.append((start, i - 1))
            start = None
    if start is not None:
        runs.append((start, len(mask) - 1))
    return runs


def detect(env_db: np.ndarray, n_samples: int, sample_rate: int,
           params: DetectParams) -> DetectionResult:
    """Turn a dB envelope into clamped clip/silence sample intervals."""
    params.validate()
    if n_samples <= 0 or env_db.size == 0:
        return DetectionResult(clips=[], silences=[],
                               noise_floor_db=float("nan"),
                               threshold_db=float("nan"))
    frame = max(1, params.ms_to_samples(params.frame_ms, sample_rate))
    hop = max(1, params.ms_to_samples(params.hop_ms, sample_rate))
    env = smooth_db(env_db, 3)
    floor = noise_floor_db(env, max(3, round(_NOISE_WINDOW_S * sample_rate / hop)))
    active = _active_mask(env, floor, params.noise_margin_db, params.hysteresis_db)

    min_clip = params.ms_to_samples(params.min_clip_ms, sample_rate)
    min_sil = params.ms_to_samples(params.min_silence_ms, sample_rate)
    guard = params.ms_to_samples(params.fade_guard_ms, sample_rate)

    # Candidate clips; short noise pulses below min_clip are dropped.
    clips = []
    for f0, f1 in _runs(active):
        s, e = f0 * hop, min(f1 * hop + frame, n_samples)
        if e - s >= min_clip:
            clips.append([s, e])
    # Merge clips separated by gaps shorter than min_silence.
    merged = []
    for s, e in clips:
        if merged and s - merged[-1][1] < min_sil:
            merged[-1][1] = e
        else:
            merged.append([s, e])
    # Fade guard: extend boundaries to cover fade-in/out, clamped to the
    # file range and to neighbouring clips so clips never overlap.
    for i, c in enumerate(merged):
        lo = merged[i - 1][1] if i else 0
        hi = merged[i + 1][0] if i + 1 < len(merged) else n_samples
        c[0] = min(max(c[0] - guard, lo), c[0])
        c[1] = max(min(c[1] + guard, hi), c[1])
    clip_iv = [Interval(s, e) for s, e in merged]

    silences = []
    prev = 0
    for iv in clip_iv:
        if iv.start_sample > prev:
            silences.append(Interval(prev, iv.start_sample))
        prev = iv.end_sample
    if prev < n_samples:
        silences.append(Interval(prev, n_samples))

    return DetectionResult(
        clips=clip_iv,
        silences=silences,
        noise_floor_db=float(np.median(floor)),
        threshold_db=float(np.median(floor) + params.noise_margin_db),
    )
