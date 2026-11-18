"""Turn raw detector ranges into final segments and exact silence coverage.

The kept segments and silence ranges form a non-overlapping partition of
``[0, total_samples)``.  Every sample that is not inside a kept segment is
counted as silence, including intervals removed by the minimum-length and
near-silence filters, so reported durations always add up.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np

from .config import DetectionConfig
from .detector import DetectionResult

_DB_FLOOR = -120.0


@dataclass(frozen=True)
class Segment:
    index: int
    start: int
    end: int
    fade_in: int
    fade_out: int
    peak_dbfs: float
    rms_dbfs: float

    @property
    def length(self) -> int:
        return self.end - self.start

    def to_dict(self, sample_rate: int) -> dict[str, object]:
        return {
            "index": self.index,
            "start_sample": self.start,
            "end_sample": self.end,
            "fade_in_samples": self.fade_in,
            "fade_out_samples": self.fade_out,
            "length_samples": self.length,
            "start_seconds": round(self.start / sample_rate, 6),
            "end_seconds": round(self.end / sample_rate, 6),
            "peak_dbfs": round(self.peak_dbfs, 2),
            "rms_dbfs": round(self.rms_dbfs, 2),
        }


@dataclass(frozen=True)
class SegmentPlan:
    segments: tuple[Segment, ...]
    silence: tuple[tuple[int, int], ...]
    total_samples: int
    sample_rate: int
    filtered_short: int
    filtered_silent: int

    @property
    def silence_samples(self) -> int:
        return sum(end - start for start, end in self.silence)

    @property
    def silence_seconds(self) -> float:
        return self.silence_samples / self.sample_rate if self.sample_rate else 0.0

    @property
    def duration_seconds(self) -> float:
        return self.total_samples / self.sample_rate if self.sample_rate else 0.0

    def silence_json(self) -> list[dict[str, object]]:
        return [
            {
                "start_sample": start,
                "end_sample": end,
                "length_samples": end - start,
            }
            for start, end in self.silence
        ]


def _dbfs(values: np.ndarray) -> float:
    if values.size == 0:
        return _DB_FLOOR
    peak = float(np.max(np.abs(values)))
    if peak <= 0.0:
        return _DB_FLOOR
    return max(20.0 * float(np.log10(peak)), _DB_FLOOR)


def _rms_dbfs(values: np.ndarray) -> float:
    if values.size == 0:
        return _DB_FLOOR
    rms = float(np.sqrt(np.mean(np.square(values))))
    if rms <= 0.0:
        return _DB_FLOOR
    return max(20.0 * float(np.log10(rms)), _DB_FLOOR)


def _complement(total: int, ranges: list[tuple[int, int]]) -> tuple[tuple[int, int], ...]:
    gaps: list[tuple[int, int]] = []
    cursor = 0
    for start, end in ranges:
        start = max(0, min(start, total))
        end = max(start, min(end, total))
        if start > cursor:
            gaps.append((cursor, start))
        cursor = max(cursor, end)
    if cursor < total:
        gaps.append((cursor, total))
    return tuple(gaps)


def finalize(
    detection: DetectionResult,
    samples: np.ndarray,
    config: DetectionConfig,
) -> SegmentPlan:
    total = detection.total_samples
    if samples.size != total:
        raise ValueError("sample vector does not match detection result")
    sample_rate = detection.sample_rate
    min_length = config.min_segment_samples(sample_rate)
    fade_limit = config.fade_samples(sample_rate)

    kept_raw: list[tuple[int, int]] = []
    filtered_short = 0
    filtered_silent = 0
    for raw in detection.segments:
        start = max(0, min(raw.start, total))
        end = max(start, min(raw.end, total))
        if end <= start:
            continue
        if end - start < min_length:
            filtered_short += 1
            continue
        if _dbfs(samples[start:end]) < config.min_peak_dbfs:
            filtered_silent += 1
            continue
        if kept_raw and start <= kept_raw[-1][1]:
            start = kept_raw[-1][1]
            if end <= start:
                filtered_short += 1
                continue
        kept_raw.append((start, end))

    segments: list[Segment] = []
    for start, end in kept_raw:
        length = end - start
        fade = min(fade_limit, length // 2)
        window = samples[start:end]
        segments.append(
            Segment(
                index=len(segments) + 1,
                start=start,
                end=end,
                fade_in=fade,
                fade_out=fade,
                peak_dbfs=_dbfs(window),
                rms_dbfs=_rms_dbfs(window),
            )
        )

    silence = _complement(total, kept_raw)
    return SegmentPlan(
        segments=tuple(segments),
        silence=silence,
        total_samples=total,
        sample_rate=sample_rate,
        filtered_short=filtered_short,
        filtered_silent=filtered_silent,
    )
