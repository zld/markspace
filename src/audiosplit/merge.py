"""Event merging, guard samples, and streaming per-segment measurement."""
from __future__ import annotations

import math
from dataclasses import dataclass

import numpy as np

from .detector import DetectedEvent
from .wav import WavInfo, WavReader


class MergeError(ValueError):
    """Raised when merge configuration cannot be applied to a file."""


@dataclass(frozen=True)
class MergeConfig:
    min_silence_ms: float = 120.0
    min_event_ms: float = 30.0
    pre_roll: int = 0
    post_roll: int = 0

    def validate(self, sample_rate: int, total_samples: int) -> None:
        if sample_rate <= 0:
            raise MergeError(f"invalid sample rate {sample_rate}")
        if total_samples <= 0:
            raise MergeError("file contains no samples")
        if not math.isfinite(self.min_silence_ms) or self.min_silence_ms < 0:
            raise MergeError("min_silence_ms must be a non-negative number")
        if not math.isfinite(self.min_event_ms) or self.min_event_ms < 0:
            raise MergeError("min_event_ms must be a non-negative number")
        for name, value in (("pre_roll", self.pre_roll), ("post_roll", self.post_roll)):
            if value < 0:
                raise MergeError(f"{name} must be non-negative")
            if value > total_samples:
                raise MergeError(
                    f"{name} of {value} samples exceeds file length {total_samples}"
                )

    def min_silence_samples(self, sample_rate: int) -> int:
        return int(round(self.min_silence_ms * sample_rate / 1000.0))

    def min_event_samples(self, sample_rate: int) -> int:
        return int(round(self.min_event_ms * sample_rate / 1000.0))


@dataclass(frozen=True)
class Segment:
    """A finalized, left-closed/right-open audio event."""

    start_sample: int
    end_sample: int
    peak: float
    rms: float
    noise_floor_db: float
    duration_ms: float

    @property
    def sample_count(self) -> int:
        return self.end_sample - self.start_sample


@dataclass(frozen=True)
class Interval:
    start: int
    end: int
    noise_floor_db: float


def build_segments(
    events: list[DetectedEvent],
    *,
    sample_rate: int,
    total_samples: int,
    config: MergeConfig,
) -> list[Segment]:
    """Expand guard samples, merge short gaps, and drop short events."""
    config.validate(sample_rate, total_samples)
    min_silence = config.min_silence_samples(sample_rate)
    min_event = config.min_event_samples(sample_rate)
    expanded = sorted(
        (
            max(0, event.start_sample - config.pre_roll),
            min(total_samples, event.end_sample + config.post_roll),
            event.noise_floor_db,
            event.peak,
            event.rms,
        )
        for event in events
    )
    merged: list[Interval] = []
    for start, end, floor, _peak, _rms in expanded:
        if end <= start:
            continue
        if merged and start - merged[-1].end <= min_silence:
            previous = merged[-1]
            if end > previous.end or floor < previous.noise_floor_db:
                merged[-1] = Interval(
                    previous.start,
                    max(end, previous.end),
                    min(floor, previous.noise_floor_db),
                )
            continue
        merged.append(Interval(start, end, floor))
    segments: list[Segment] = []
    for interval in merged:
        if interval.end - interval.start < min_event:
            continue
        segments.append(
            Segment(
                start_sample=interval.start,
                end_sample=interval.end,
                peak=0.0,
                rms=0.0,
                noise_floor_db=interval.noise_floor_db,
                duration_ms=(interval.end - interval.start) * 1000.0 / sample_rate,
            )
        )
    return segments


def measure(
    path: str,
    info: WavInfo,
    segments: list[Segment],
) -> list[Segment]:
    """Fill peak/RMS for every segment with one streaming file pass."""
    if not segments:
        return []
    peaks = np.zeros(len(segments), dtype=np.float64)
    sums = np.zeros(len(segments), dtype=np.float64)
    counts = np.zeros(len(segments), dtype=np.int64)
    cursor = 0
    position = 0
    for block in WavReader(path).iter_blocks(8192):
        block_start = position
        block_end = position + block.shape[0]
        position = block_end
        while cursor < len(segments):
            segment = segments[cursor]
            if segment.end_sample <= block_start:
                cursor += 1
                continue
            if segment.start_sample >= block_end:
                break
            lo = max(segment.start_sample, block_start) - block_start
            hi = min(segment.end_sample, block_end) - block_start
            mono = block[lo:hi].mean(axis=1, dtype=np.float64) / 32768.0
            peaks[cursor] = max(peaks[cursor], float(np.max(np.abs(mono))))
            sums[cursor] += float(np.sum(np.square(mono)))
            counts[cursor] += hi - lo
            if segment.end_sample <= block_end:
                cursor += 1
                continue
            break  # segment continues into the next block
        if cursor >= len(segments):
            break
    result: list[Segment] = []
    for index, segment in enumerate(segments):
        count = int(counts[index])
        result.append(
            Segment(
                start_sample=segment.start_sample,
                end_sample=segment.end_sample,
                peak=peaks[index] if count else 0.0,
                rms=math.sqrt(sums[index] / count) if count else 0.0,
                noise_floor_db=segment.noise_floor_db,
                duration_ms=segment.duration_ms,
            )
        )
    return result
