"""PCM beep synthesis and audio/video alignment measurement.

Beep onsets are placed at exact sample indices; each onset sample time
is mapped to the nearest video frame and the absolute offset between
the sample time and that frame's start time is reported. The worst
case over all beeps is max_abs_error_ms (bounded by half a frame).
"""

from __future__ import annotations

from fractions import Fraction

import numpy as np

from .model import Timeline
from .timebase import nearest_frame


def synth_beeps(timeline: Timeline) -> np.ndarray:
    """Render the timeline's beeps into a mono int16 PCM buffer."""
    rate = timeline.sample_rate
    total = int(round(float(timeline.duration) * rate))
    if total <= 0 or not timeline.beeps:
        return np.zeros(max(total, 0), dtype=np.int16)
    pcm = np.zeros(total, dtype=np.float64)
    for beep in timeline.beeps:
        start = int(round(float(beep.time) * rate))
        length = int(round(float(beep.duration) * rate))
        if start >= total or length <= 0:
            continue
        length = min(length, total - start)
        t = np.arange(length, dtype=np.float64) / rate
        tone = np.sin(2.0 * np.pi * beep.freq * t)
        # 5 ms raised-cosine edges to avoid clicks.
        edge = min(length // 2, int(0.005 * rate))
        env = np.ones(length, dtype=np.float64)
        if edge > 0:
            ramp = 0.5 - 0.5 * np.cos(np.pi * np.arange(edge) / edge)
            env[:edge] = ramp
            env[-edge:] = ramp[::-1]
        pcm[start : start + length] += 0.3 * tone * env
    pcm = np.clip(pcm, -1.0, 1.0)
    return np.rint(pcm * 32767.0).astype(np.int16)


def beep_onset_samples(timeline: Timeline) -> list[int]:
    """Sample index of each beep onset (rounded to the nearest sample)."""
    rate = timeline.sample_rate
    return [int(round(float(b.time) * rate)) for b in timeline.beeps]


def max_alignment_error_ms(timeline: Timeline) -> float:
    """Worst |sample_time - nearest_frame_start| over all beeps, in ms."""
    worst = Fraction(0)
    rate = timeline.sample_rate
    for sample in beep_onset_samples(timeline):
        t_sample = Fraction(sample, rate)
        frame = nearest_frame(timeline.fps, t_sample)
        t_frame = Fraction(frame) / timeline.fps
        err = abs(t_sample - t_frame)
        if err > worst:
            worst = err
    return float(worst * 1000)
