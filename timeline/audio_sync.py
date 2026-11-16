"""Synthetic audio cue clock and A/V alignment statistics.

Cue bursts are rendered into a sample buffer whose clock drifts by
``drift_ppm`` relative to the video clock, then re-detected assuming the
nominal sample rate. The per-cue misalignment (start offset plus drift
accumulation) is reported in milliseconds.
"""
from __future__ import annotations

import math
from dataclasses import dataclass

import numpy as np

from .clock import FrameClock

MIN_SAMPLE_RATE = 8000
MAX_SAMPLE_RATE = 192000
MAX_OFFSET_MS = 5000.0
MAX_DRIFT_PPM = 1000.0


class AudioSyncError(ValueError):
    """Raised when audio cue synthesis or alignment fails."""


@dataclass(frozen=True)
class AudioCueConfig:
    sample_rate: int
    offset_ms: float
    drift_ppm: float

    def __post_init__(self) -> None:
        if not MIN_SAMPLE_RATE <= self.sample_rate <= MAX_SAMPLE_RATE:
            raise AudioSyncError(
                f"sample_rate {self.sample_rate} out of range "
                f"[{MIN_SAMPLE_RATE}, {MAX_SAMPLE_RATE}]"
            )
        if not 0.0 <= self.offset_ms <= MAX_OFFSET_MS:
            raise AudioSyncError(
                f"offset_ms {self.offset_ms} out of range [0, {MAX_OFFSET_MS}]"
            )
        if not -MAX_DRIFT_PPM <= self.drift_ppm <= MAX_DRIFT_PPM:
            raise AudioSyncError(
                f"drift_ppm {self.drift_ppm} out of range "
                f"[{-MAX_DRIFT_PPM}, {MAX_DRIFT_PPM}]"
            )


@dataclass(frozen=True)
class SyncStats:
    sample_rate: int
    offset_ms: float
    drift_ppm: float
    cues_expected: int
    cues_detected: int
    av_max_error_ms: float
    av_mean_error_ms: float


def synth_cue_track(
    frame_times_s: list[float], config: AudioCueConfig, tone_hz: float = 880.0
) -> np.ndarray:
    """Render one cosine burst per frame time into the drifted sample clock."""
    drift = 1.0 + config.drift_ppm / 1e6
    sr = config.sample_rate
    offset_s = config.offset_ms / 1000.0
    interval_s = frame_times_s[1] - frame_times_s[0] if len(frame_times_s) > 1 else 1.0
    burst_len = max(1, min(int(0.02 * sr), int(0.5 * interval_s * sr)))
    starts = [
        int(round((t + offset_s) * sr * drift)) for t in frame_times_s
    ]
    total = starts[-1] + burst_len + 1
    track = np.zeros(total, dtype=np.float64)
    phase = 2.0 * math.pi * tone_hz / sr
    burst = np.cos(phase * np.arange(burst_len))
    for start in starts:
        track[start : start + burst_len] = burst
    return track


def detect_onsets(
    track: np.ndarray, min_gap: int, threshold: float = 0.5
) -> list[int]:
    """Return the start sample of each cue burst in ``track``.

    Samples above ``threshold`` are grouped into bursts; indices closer
    than ``min_gap`` samples belong to the same burst (a tone oscillates
    across the threshold many times within one burst).
    """
    above = np.flatnonzero(np.abs(track) > threshold)
    if above.size == 0:
        return []
    groups = np.split(above, np.flatnonzero(np.diff(above) > min_gap) + 1)
    return [int(group[0]) for group in groups]


def measure_alignment(
    clock: FrameClock, frame_count: int, config: AudioCueConfig
) -> SyncStats:
    """Synthesize cues for every frame and measure A/V misalignment."""
    if frame_count < 1:
        raise AudioSyncError("cannot align a zero-frame timeline")
    frame_times = [float(clock.frame_start(i)) for i in range(frame_count)]
    track = synth_cue_track(frame_times, config)
    interval_s = frame_times[1] - frame_times[0] if frame_count > 1 else 1.0
    min_gap = max(1, int(0.25 * interval_s * config.sample_rate))
    onsets = detect_onsets(track, min_gap)
    if len(onsets) != frame_count:
        raise AudioSyncError(
            f"detected {len(onsets)} cues, expected {frame_count}"
        )
    errors_ms = [
        (onsets[k] / config.sample_rate - frame_times[k]) * 1000.0
        for k in range(frame_count)
    ]
    return SyncStats(
        sample_rate=config.sample_rate,
        offset_ms=config.offset_ms,
        drift_ppm=config.drift_ppm,
        cues_expected=frame_count,
        cues_detected=len(onsets),
        av_max_error_ms=round(max(abs(e) for e in errors_ms), 3),
        av_mean_error_ms=round(sum(errors_ms) / len(errors_ms), 3),
    )
