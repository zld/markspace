"""Segment shaping: fade-aware boundary trimming, merging, duration filters.

Raw hysteresis regions (frame units) are refined here to exact sample
positions. Fade-in/out boundaries are first extended along the frame-energy
trend (energy decays smoothly into the noise floor), then snapped to sample
precision by re-scanning a small window of the source through ``read_window``.
Short bursts are dropped and close regions merged according to SegmentParams.
"""
from __future__ import annotations

from dataclasses import dataclass
from typing import Callable

import numpy as np

from .detector import DetectionResult

ReadWindow = Callable[[int, int], np.ndarray]  # (start, end) -> mono samples


@dataclass(frozen=True)
class SegmentParams:
    min_event_ms: float = 120.0   # shorter activity is treated as a burst
    min_silence_ms: float = 250.0  # gaps shorter than this are absorbed
    merge_gap_ms: float = 80.0    # raw regions closer than this merge first
    fade_margin: float = 1.1      # extend while rms > noise_floor * margin
    fade_trend: float = 1.5       # tolerated non-monotonicity along a fade
    refine_subwindows: int = 8    # sub-windows per frame for sample snapping

    def validate(self) -> None:
        if self.min_event_ms <= 0:
            raise ValueError("min_event_ms must be positive")
        if self.min_silence_ms < 0 or self.merge_gap_ms < 0:
            raise ValueError("min_silence_ms and merge_gap_ms must be >= 0")
        if self.fade_margin < 1.0:
            raise ValueError("fade_margin must be >= 1.0")
        if self.fade_trend < 1.0:
            raise ValueError("fade_trend must be >= 1.0")
        if self.refine_subwindows < 1:
            raise ValueError("refine_subwindows must be >= 1")


@dataclass(frozen=True)
class Segment:
    start_sample: int
    end_sample: int  # exclusive
    sample_rate: int

    @property
    def duration_samples(self) -> int:
        return self.end_sample - self.start_sample

    @property
    def duration_seconds(self) -> float:
        return self.duration_samples / self.sample_rate


def _extend_fade(rms: np.ndarray, idx: int, floor: float, direction: int,
                 margin: float, trend: float) -> int:
    """Walk along a fade while energy stays above floor*margin and keeps
    decaying toward the noise (tolerating small local bumps)."""
    limit = floor * margin
    i = idx
    if direction < 0:
        while i > 0 and rms[i - 1] > limit and rms[i - 1] <= rms[i] * trend:
            i -= 1
    else:
        last = rms.size - 1
        while i < last and rms[i + 1] > limit and rms[i + 1] <= rms[i] * trend:
            i += 1
    return i


def _snap_edge(read_window: ReadWindow, approx: int, radius: int, level: float,
               want_first: bool, subwindows: int, num_samples: int) -> int:
    """Refine a boundary near ``approx`` to sub-frame (sample-level) precision
    by locating the first/last sub-window whose RMS reaches ``level``."""
    lo = max(0, approx - radius)
    hi = min(num_samples, approx + radius)
    win = read_window(lo, hi)
    sub = max(1, win.size // max(1, subwindows))
    count = win.size // sub
    if count == 0:
        return approx
    r = np.sqrt(np.mean(win[: count * sub].reshape(count, sub) ** 2, axis=1))
    hits = np.nonzero(r >= level)[0]
    if hits.size == 0:
        return approx
    if want_first:
        return lo + int(hits[0]) * sub
    return min(num_samples, lo + (int(hits[-1]) + 1) * sub)


def _merge(spans: list[tuple[int, int]], gap: int) -> list[tuple[int, int]]:
    if not spans:
        return []
    spans = sorted(spans)
    out = [spans[0]]
    for s, e in spans[1:]:
        ps, pe = out[-1]
        if s - pe <= gap:
            out[-1] = (ps, max(pe, e))
        else:
            out.append((s, e))
    return out


def build_segments(result: DetectionResult, params: SegmentParams,
                   read_window: ReadWindow) -> list[Segment]:
    """Turn raw hysteresis regions into sample-accurate, filtered segments."""
    params.validate()
    sr = result.sample_rate
    hop, frame, total = result.hop, result.frame, result.num_samples
    rms = result.frame_rms
    level = result.low_threshold
    radius = frame  # search one frame to each side when snapping

    spans: list[tuple[int, int]] = []
    for region in result.regions:
        f0 = _extend_fade(rms, region.start_frame, result.noise_floor, -1,
                          params.fade_margin, params.fade_trend)
        f1 = _extend_fade(rms, region.end_frame - 1, result.noise_floor, +1,
                          params.fade_margin, params.fade_trend)
        start = _snap_edge(read_window, f0 * hop, radius, level, True,
                           params.refine_subwindows, total)
        end = _snap_edge(read_window, min(total, f1 * hop + frame), radius,
                         level, False, params.refine_subwindows, total)
        start = max(0, min(start, total))
        end = max(start, min(end, total))
        if end > start:
            spans.append((start, end))

    merge_gap = round(params.merge_gap_ms * sr / 1000.0)
    min_event = round(params.min_event_ms * sr / 1000.0)
    min_silence = round(params.min_silence_ms * sr / 1000.0)

    spans = _merge(spans, merge_gap)
    spans = [s for s in spans if s[1] - s[0] >= min_event]  # drop short bursts
    spans = _merge(spans, min_silence)
    return [Segment(s, e, sr) for s, e in spans]
