"""Adaptive energy-based segmentation.

Pipeline per file:
  1. frame the audio and compute combined multi-channel RMS energy (dB)
  2. estimate the noise floor with an asymmetric tracker (fast attack on
     quieter frames, slow release so sustained sound is not mistaken for
     noise) plus a global-minimum safety floor for clips that start loud
  3. walk a hysteresis state machine (high threshold opens, low closes)
  4. backtrack fade-in boundaries to the low threshold, so slow fades and
     weak events above the floor are kept instead of clipped
  5. merge runs separated by short silences, drop short runs, pad and clamp
All boundaries are half-open sample intervals [start_sample, end_sample).
"""
from __future__ import annotations

from collections import deque

import numpy as np

from .models import AudioData, DetectParams, DetectionResult, Segment, Silence

_EPS_ENERGY = 1e-12  # keeps log10 finite for digital silence
_HYSTERESIS_DB = 3.0
_ALPHA_FAST = 0.35  # downward adaptation (quieter frame encountered)
_MIN_WINDOW_MS = 500.0
_MIN_WINDOW_FRAMES = 5
# Fallback pass for constant-level clips without any quiet reference frame:
# armed on a wide level spread, or on a perfectly flat non-digital-silence
# signal (e.g. a steady tone the adaptive floor locks onto); uniform noise
# shows a small but non-zero spread and stays excluded.
_SPREAD_TRIGGER_DB = 6.0
_FLAT_LEVEL_DB = -40.0
_FALLBACK_TRIGGER_DB = 8.0


def _frame_energies(audio: AudioData, frame_len: int) -> tuple[np.ndarray, list[int]]:
    """Return per-frame mean-square energy (all channels) and frame end samples."""
    total = audio.total_samples
    n_full = total // frame_len
    full = audio.samples[: n_full * frame_len].reshape(n_full, frame_len, -1)
    energies = np.mean(full.astype(np.float64) ** 2, axis=(1, 2))
    ends = [min((k + 1) * frame_len, total) for k in range(n_full)]
    remainder = total - n_full * frame_len
    if remainder:
        tail = audio.samples[n_full * frame_len :]
        energies = np.append(energies, np.mean(tail.astype(np.float64) ** 2))
        ends.append(total)
    return energies, ends


def _sliding_min(values: np.ndarray, window: int) -> np.ndarray:
    """Trailing-window minimum via a monotonic deque."""
    out = np.empty_like(values)
    dq: deque[int] = deque()
    for i, value in enumerate(values):
        while dq and values[dq[-1]] >= value:
            dq.pop()
        dq.append(i)
        if dq[0] <= i - window:
            dq.popleft()
        out[i] = values[dq[0]]
    return out


def _noise_floor(db: np.ndarray, window: int) -> np.ndarray:
    """Combined noise estimate.

    The sliding minimum gives the true floor once a quiet frame exists inside
    the window. The downward-only tracker covers clips that are loud from the
    first frame (no quiet reference for the sliding minimum) by adapting
    quickly whenever the level drops. Taking the minimum of the two keeps the
    estimate on the quiet side, so thresholds never track the signal itself.
    """
    tracker = np.empty_like(db)
    estimate = db[0]
    for k, level in enumerate(db):
        if level < estimate:
            estimate = _ALPHA_FAST * level + (1.0 - _ALPHA_FAST) * estimate
        tracker[k] = estimate
    return np.minimum(tracker, _sliding_min(db, window))


def _sound_runs(db: np.ndarray, high: np.ndarray, low: np.ndarray) -> list[tuple[int, int]]:
    """Hysteresis state machine; returns half-open frame index ranges.

    A frame opens a run above ``high``; run boundaries backtrack to ``low`` on
    both sides, so slow fade-ins and fade-outs are kept instead of clipped.
    """
    runs: list[tuple[int, int]] = []
    in_sound = False
    start = 0
    for k in range(db.size):
        if not in_sound:
            if db[k] >= high[k]:
                start = k
                while start > 0 and db[start - 1] > low[start - 1]:
                    start -= 1  # fade-in backtrack to low threshold
                in_sound = True
        elif db[k] < low[k]:
            end = k
            while end < db.size and db[end] > low[end]:
                end += 1  # extend over the contiguous fade-out tail only
            runs.append((start, end))
            in_sound = False
    if in_sound:
        runs.append((start, db.size))
    return runs


def detect(audio: AudioData, params: DetectParams) -> DetectionResult:
    """Analyse one audio buffer and return segments plus silence regions."""
    params.validate()
    sr = audio.sample_rate
    frame_len = max(1, round(params.frame_ms * sr / 1000.0))
    energies, frame_ends = _frame_energies(audio, frame_len)
    db = 10.0 * np.log10(energies + _EPS_ENERGY)

    window = max(_MIN_WINDOW_FRAMES, int(_MIN_WINDOW_MS / params.frame_ms))
    noise_floor = _noise_floor(db, min(window, db.size))
    high = noise_floor + params.noise_margin_db
    low = high - _HYSTERESIS_DB
    runs = _sound_runs(db, high, low)

    # Fallback: adaptive thresholds find nothing. If the clip still spans a
    # wide level range the noise estimator is likely locked onto constant
    # signal; a perfectly flat signal well above digital silence is handled
    # the same way. Rescan against an absolute floor from the file minimum.
    if not runs:
        file_min = float(np.min(db))
        spread = float(np.percentile(db, 95) - file_min)
        if spread == 0.0 and file_min >= _FLAT_LEVEL_DB:
            runs = [(0, db.size)]  # steady tone: the whole clip is one segment
        elif spread >= _SPREAD_TRIGGER_DB:
            abs_high = np.full_like(db, file_min + _FALLBACK_TRIGGER_DB)
            abs_low = abs_high - _HYSTERESIS_DB
            runs = _sound_runs(db, abs_high, abs_low)

    min_silence_samples = round(params.min_silence_ms * sr / 1000.0)
    min_segment_samples = round(params.min_segment_ms * sr / 1000.0)
    pad_samples = round(params.pad_ms * sr / 1000.0)
    total = audio.total_samples

    # Frame ranges -> sample intervals, merging across short silences.
    merged: list[list[int]] = []
    for run_start, run_end in runs:
        start_sample = run_start * frame_len
        end_sample = frame_ends[run_end - 1]
        if merged and start_sample - merged[-1][1] < min_silence_samples:
            merged[-1][1] = end_sample
        else:
            merged.append([start_sample, end_sample])

    # Drop runs shorter than the minimum segment length, then pad/clamp.
    segments: list[Segment] = []
    for start_sample, end_sample in merged:
        if end_sample - start_sample < min_segment_samples:
            continue
        segments.append(
            Segment(
                start_sample=max(0, start_sample - pad_samples),
                end_sample=min(total, end_sample + pad_samples),
            )
        )

    silences = _complement_silences(segments, total)
    return DetectionResult(
        segments=segments,
        silences=silences,
        sample_rate=sr,
        total_samples=total,
        noise_floor_db=float(np.median(noise_floor)),
    )


def _complement_silences(segments: list[Segment], total: int) -> list[Silence]:
    """Silence is the complement of the segments within [0, total)."""
    silences: list[Silence] = []
    cursor = 0
    for seg in segments:
        if seg.start_sample > cursor:
            silences.append(Silence(cursor, seg.start_sample))
        cursor = max(cursor, seg.end_sample)
    if cursor < total:
        silences.append(Silence(cursor, total))
    return silences
