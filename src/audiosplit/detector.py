"""Frame-based energy detection with a sliding local noise floor.

The detector is deliberately independent of the CLI and store: it consumes a
mono float64 signal plus a validated :class:`DetectionConfig` and returns raw
sample ranges.  Final boundary policy (fades, filtering, silence) lives in
``segments.py``.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np

from .config import DetectionConfig

_DB_FLOOR = -120.0


class DetectionError(Exception):
    """Raised when detection cannot proceed."""


@dataclass(frozen=True)
class FrameEnvelope:
    rms: np.ndarray
    peak: np.ndarray
    counts: np.ndarray
    frame_samples: int

    @property
    def count(self) -> int:
        return int(self.rms.size)

    def rms_db(self) -> np.ndarray:
        return _to_db(self.rms)

    def peak_db(self) -> np.ndarray:
        return _to_db(self.peak)

    def start_sample(self, frame: int) -> int:
        return int(frame) * self.frame_samples

    def end_sample(self, frame: int, total: int) -> int:
        return min((int(frame) + 1) * self.frame_samples, total)


@dataclass(frozen=True)
class RawSegment:
    start: int
    end: int
    onset_frame: int
    end_frame: int


@dataclass(frozen=True)
class DetectionResult:
    envelope: FrameEnvelope
    noise_floor_db: np.ndarray
    high_db: np.ndarray
    low_db: np.ndarray
    segments: tuple[RawSegment, ...]
    total_samples: int
    sample_rate: int


def _to_db(values: np.ndarray) -> np.ndarray:
    return np.maximum(20.0 * np.log10(np.maximum(values, 1e-12)), _DB_FLOOR)


def frame_signal(samples: np.ndarray, frame_samples: int) -> FrameEnvelope:
    if samples.ndim != 1:
        raise DetectionError("expected a mono sample vector")
    if frame_samples < 2:
        raise DetectionError("frame length must be at least two samples")
    total = int(samples.size)
    if total == 0:
        empty = np.zeros(0, dtype=np.float64)
        return FrameEnvelope(empty, empty.copy(), np.zeros(0, dtype=np.int64), frame_samples)
    frame_count = (total + frame_samples - 1) // frame_samples
    padded = np.zeros(frame_count * frame_samples, dtype=np.float64)
    padded[:total] = samples
    blocks = padded.reshape(frame_count, frame_samples)
    sumsq = np.einsum("ij,ij->i", blocks, blocks)
    peak = np.max(np.abs(blocks), axis=1)
    counts = np.full(frame_count, frame_samples, dtype=np.int64)
    counts[-1] = total - (frame_count - 1) * frame_samples
    rms = np.sqrt(sumsq / counts)
    return FrameEnvelope(rms=rms, peak=peak, counts=counts, frame_samples=frame_samples)


def _sliding_percentile(values: np.ndarray, window: int, percentile: float) -> np.ndarray:
    count = int(values.size)
    if count == 0:
        return np.zeros(0, dtype=np.float64)
    window = max(1, min(int(window), count))
    before = (window - 1) // 2
    after = window - 1 - before
    padded = np.pad(values, (before, after), mode="edge")
    windows = np.lib.stride_tricks.sliding_window_view(padded, window)
    out = np.empty(count, dtype=np.float64)
    chunk = 8192
    for start in range(0, count, chunk):
        stop = min(start + chunk, count)
        out[start:stop] = np.percentile(windows[start:stop], percentile, axis=1)
    return out


def estimate_noise_floor(
    envelope: FrameEnvelope, config: DetectionConfig
) -> np.ndarray:
    window = config.noise_window_frames()
    rms_floor = _sliding_percentile(envelope.rms, window, config.noise_percentile)
    return _to_db(rms_floor)


def _onset_frames(high_db: np.ndarray, required: int) -> list[int]:
    runs: list[int] = []
    streak = 0
    for index, value in enumerate(high_db):
        if value >= 0.0:
            streak += 1
            if streak == required:
                runs.append(index - required + 1)
        else:
            streak = 0
    return runs


def _backtrack_fade_in(
    onset: int, rms_db: np.ndarray, low_db: np.ndarray, lower_bound: int
) -> int:
    index = onset
    while index > lower_bound and rms_db[index - 1] >= low_db[index - 1]:
        index -= 1
    return index


def _extend_tail(
    start: int, rms_db: np.ndarray, low_db: np.ndarray, release: int
) -> int:
    count = int(rms_db.size)
    last_loud = start
    index = start + 1
    while index < count and rms_db[index] >= low_db[index]:
        last_loud = index
        index += 1
    return min(count, last_loud + 1 + release)


def _merge(
    segments: list[RawSegment], merge_samples: int, total: int
) -> list[RawSegment]:
    if not segments:
        return []
    merged: list[RawSegment] = []
    for segment in segments:
        if (
            merged
            and segment.start - merged[-1].end < merge_samples
        ):
            previous = merged[-1]
            merged[-1] = RawSegment(
                start=previous.start,
                end=max(previous.end, segment.end),
                onset_frame=previous.onset_frame,
                end_frame=segment.end_frame,
            )
        else:
            merged.append(segment)
    return [
        RawSegment(
            start=max(0, item.start),
            end=min(total, item.end),
            onset_frame=item.onset_frame,
            end_frame=item.end_frame,
        )
        for item in merged
        if item.end > item.start
    ]


def detect(
    samples: np.ndarray, sample_rate: int, config: DetectionConfig
) -> DetectionResult:
    config.validate()
    config.validate_sample_rate(sample_rate)
    if samples.ndim != 1:
        raise DetectionError("expected a mono sample vector")
    if samples.size and not np.all(np.isfinite(samples)):
        raise DetectionError("samples contain non-finite values")
    total = int(samples.size)
    envelope = frame_signal(samples, config.frame_samples(sample_rate))
    floor_db = estimate_noise_floor(envelope, config)
    rms_db = envelope.rms_db()
    high_db = floor_db + config.high_margin_db
    low_db = floor_db + config.low_margin_db
    above_high = rms_db - high_db
    merge_samples = config.merge_samples(sample_rate)

    raw: list[RawSegment] = []
    lower_bound = 0
    for confirmed in _onset_frames(above_high, config.onset_frames):
        start_frame = _backtrack_fade_in(confirmed, rms_db, low_db, lower_bound)
        end_frame = _extend_tail(confirmed, rms_db, low_db, config.release_frames)
        if end_frame * envelope.frame_samples <= start_frame * envelope.frame_samples:
            continue
        raw.append(
            RawSegment(
                start=envelope.start_sample(start_frame),
                end=envelope.end_sample(end_frame - 1, total),
                onset_frame=start_frame,
                end_frame=end_frame,
            )
        )
        lower_bound = end_frame

    return DetectionResult(
        envelope=envelope,
        noise_floor_db=floor_db,
        high_db=high_db,
        low_db=low_db,
        segments=tuple(_merge(raw, merge_samples, total)),
        total_samples=total,
        sample_rate=sample_rate,
    )
