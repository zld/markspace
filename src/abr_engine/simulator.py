"""Segment-by-segment download/playback simulation over a network trace."""
from __future__ import annotations

from dataclasses import dataclass

from .estimator import DownloadSample, ThroughputEstimator
from .model import BufferState, DecisionRecord, StreamConfig
from .policy import RatePolicy
from .trace import NetworkTrace


class SimulationError(RuntimeError):
    """Simulation cannot proceed (CLI exit code 1)."""


@dataclass(frozen=True)
class SimulationResult:
    records: tuple[DecisionRecord, ...]
    switch_count: int
    total_rebuffer_s: float
    final_buffer_s: float


def download_duration(
    trace: NetworkTrace, start_t_s: float, size_bits: float
) -> tuple[float, bool]:
    """Time to transfer size_bits starting at start_t_s.

    Walks the trace sample by sample, splitting the transfer across every
    sample interval the download spans instead of assuming one bandwidth.
    Returns (duration_s, crossed_outage). The last sample persists beyond
    the trace; if it is an outage the download can never finish.
    """
    if size_bits <= 0:
        raise SimulationError("segment size must be positive")
    samples = trace.samples
    last = len(samples) - 1
    idx = trace.sample_index_at(start_t_s)
    # Connection setup latency from the RTT where the download starts.
    t = start_t_s + samples[idx].rtt_ms / 1000.0
    idx = trace.sample_index_at(t)
    remaining = float(size_bits)
    crossed_outage = False
    while remaining > 0.0:
        sample = samples[idx]
        rate = 0.0 if sample.outage else sample.bandwidth_bps
        if idx == last:
            # Last sample extends indefinitely.
            if rate <= 0.0:
                raise SimulationError(
                    "trace ends during an outage; download cannot complete"
                )
            t += remaining / rate
            remaining = 0.0
            break
        interval_end = sample.t_s + trace.dt_s
        span = interval_end - t
        if span <= 0.0:  # numeric edge: already past this sample
            idx += 1
            continue
        if rate <= 0.0:
            crossed_outage = True
            t = interval_end
            idx += 1
            continue
        capacity = rate * span
        if capacity >= remaining:
            t += remaining / rate
            remaining = 0.0
        else:
            remaining -= capacity
            t = interval_end
            idx += 1
    return t - start_t_s, crossed_outage


def simulate(
    trace: NetworkTrace,
    stream: StreamConfig,
    policy: RatePolicy,
    estimator: ThroughputEstimator,
) -> SimulationResult:
    """Run the full loop: decide, download, play out, record, repeat."""
    buffer = BufferState(level_s=stream.initial_buffer_s, max_s=stream.max_buffer_s)
    records: list[DecisionRecord] = []
    now_s = 0.0
    switches = 0
    prev_bitrate: int | None = None
    for i in range(stream.segment_count):
        estimated = estimator.estimate_bps()
        bitrate = policy.decide(estimated, buffer.level_s)
        if prev_bitrate is not None and bitrate != prev_bitrate:
            switches += 1
        prev_bitrate = bitrate

        size_bits = float(bitrate) * stream.segment_duration_s
        download_s, crossed_outage = download_duration(trace, now_s, size_bits)
        stall_s = buffer.consume(download_s)
        buffer.add_segment(stream.segment_duration_s)
        estimator.add_sample(
            DownloadSample(
                bytes_transferred=int(size_bits // 8),
                duration_s=download_s,
                crossed_outage=crossed_outage,
            )
        )
        records.append(
            DecisionRecord(
                segment=i,
                bitrate_bps=bitrate,
                estimated_bps=estimated,
                download_s=download_s,
                buffer_s=buffer.level_s,
                rebuffer_s=stall_s,
            )
        )
        now_s += download_s
    return SimulationResult(
        records=tuple(records),
        switch_count=switches,
        total_rebuffer_s=buffer.total_rebuffer_s,
        final_buffer_s=buffer.level_s,
    )
