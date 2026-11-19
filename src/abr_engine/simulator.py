"""Playback simulation coordinating trace, estimator and policy."""
from __future__ import annotations

import bisect
import math
from dataclasses import dataclass, field

from .estimator import ThroughputEstimator
from .models import (
    NetworkSample,
    Segment,
    SegmentRecord,
    ValidationError,
    require_finite,
    validate_ladder,
)
from .policy import AbrPolicy


class SimulationError(RuntimeError):
    """Raised when simulation cannot proceed despite valid inputs."""


@dataclass
class SimulationResult:
    records: list[SegmentRecord]
    continuation_intervals: list[tuple[float, float]] = field(default_factory=list)
    final_buffer_s: float = 0.0


def _validate_trace(samples: list[NetworkSample]) -> None:
    if not samples:
        raise ValidationError("network trace is empty")
    for i in range(1, len(samples)):
        if samples[i].timestamp_s < samples[i - 1].timestamp_s:
            raise ValidationError(
                f"trace timestamp regression at index {i}: "
                f"{samples[i].timestamp_s} < {samples[i - 1].timestamp_s}"
            )


def _sample_at(samples: list[NetworkSample], timestamps: list[float], t: float) -> NetworkSample:
    i = bisect.bisect_right(timestamps, t) - 1
    return samples[max(0, i)]


def _download_seconds(
    samples: list[NetworkSample],
    timestamps: list[float],
    t_start: float,
    bits: float,
) -> tuple[float, tuple[float, float] | None]:
    """Integrate trace bandwidth from t_start until bits are delivered.

    Returns (duration_s, continuation) where continuation is the
    (start, end) interval beyond the trace end served by the last
    sample, or None when the trace covers the whole download.
    """
    remaining = bits
    t = t_start
    i = max(0, bisect.bisect_right(timestamps, t) - 1)
    last = len(samples) - 1
    while True:
        bandwidth = samples[i].bandwidth_bps
        if i >= last:
            end = t + remaining / bandwidth
            continuation = None
            if end > timestamps[-1]:
                continuation = (max(t, timestamps[-1]), end)
            return end - t_start, continuation
        nxt = timestamps[i + 1]
        if nxt <= t:  # zero-length interval from duplicate timestamps
            i += 1
            continue
        chunk = bandwidth * (nxt - t)
        if chunk >= remaining:
            return t + remaining / bandwidth - t_start, None
        remaining -= chunk
        t = nxt
        i += 1


def simulate(
    samples: list[NetworkSample],
    bitrates,
    segments: int,
    segment_duration_s: float,
    initial_buffer_s: float,
    *,
    buffer_capacity_s: float | None = None,
    segment_bytes: float | None = None,
    estimator: ThroughputEstimator | None = None,
    policy: AbrPolicy | None = None,
) -> SimulationResult:
    """Simulate segment-by-segment ABR playback over a network trace."""
    _validate_trace(samples)
    ladder = validate_ladder(bitrates)
    if isinstance(segments, bool) or not isinstance(segments, int) or segments <= 0:
        raise ValidationError(f"segments must be a positive integer, got {segments!r}")
    duration = require_finite("segment_duration_s", segment_duration_s)
    if duration <= 0.0:
        raise ValidationError(f"segment_duration_s must be > 0, got {duration}")
    buffer_s = require_finite("initial_buffer_s", initial_buffer_s)
    if buffer_s < 0.0:
        raise ValidationError(f"initial_buffer_s must be >= 0, got {buffer_s}")
    capacity = None
    if buffer_capacity_s is not None:
        capacity = require_finite("buffer_capacity_s", buffer_capacity_s)
        if capacity < duration:
            raise ValidationError(
                f"buffer_capacity_s {capacity} must be >= segment duration {duration}"
            )
    if segment_bytes is not None:
        segment_bytes = require_finite("segment_bytes", segment_bytes)
        if segment_bytes <= 0.0:
            raise ValidationError(f"segment_bytes must be > 0, got {segment_bytes}")
        for bitrate in ladder:
            computed = bitrate * duration / 8.0
            if abs(segment_bytes - computed) > max(1.0, Segment.SIZE_TOLERANCE * computed):
                raise ValidationError(
                    f"segment_bytes {segment_bytes} conflicts with computed size "
                    f"{computed} for bitrate {bitrate}; provide only one of them"
                )

    estimator = estimator or ThroughputEstimator()
    policy = policy or AbrPolicy(ladder)
    timestamps = [s.timestamp_s for s in samples]

    records: list[SegmentRecord] = []
    continuations: list[tuple[float, float]] = []
    next_idx = 0
    clock = 0.0
    for index in range(segments):
        while next_idx < len(samples) and samples[next_idx].timestamp_s <= clock:
            estimator.add_sample(samples[next_idx])
            next_idx += 1
        estimate = estimator.estimate(clock)
        net = _sample_at(samples, timestamps, clock)
        decision = policy.select(
            now_s=clock,
            estimate=estimate,
            rtt_ms=net.rtt_ms,
            loss_rate=net.loss_rate,
            buffer_s=buffer_s,
        )
        segment = Segment(
            index=index,
            bitrate_bps=ladder[decision.level],
            duration_s=duration,
            size_bytes=segment_bytes,
        )
        bits = segment.resolved_size_bytes() * 8.0
        download_s, continuation = _download_seconds(samples, timestamps, clock, bits)
        if continuation is not None:
            continuations.append(continuation)

        buffer_before = buffer_s
        buffer_s -= download_s
        stall_s = 0.0
        if buffer_s < 0.0:
            stall_s = -buffer_s
            buffer_s = 0.0
        buffer_s += duration
        if capacity is not None:
            buffer_s = min(buffer_s, capacity)
        start_s = clock
        clock += download_s

        values = (estimate.bandwidth_bps, buffer_before, buffer_s, download_s, stall_s)
        if not all(math.isfinite(v) for v in values):
            raise SimulationError(f"non-finite playback state at segment {index}")
        records.append(
            SegmentRecord(
                index=index,
                time_s=start_s,
                estimated_bandwidth_bps=estimate.bandwidth_bps,
                confidence=estimate.confidence,
                buffer_before_s=buffer_before,
                buffer_after_s=buffer_s,
                selected_bitrate_bps=ladder[decision.level],
                download_s=download_s,
                switch=decision.switch,
                stall_s=stall_s,
            )
        )
    return SimulationResult(records, continuations, buffer_s)
