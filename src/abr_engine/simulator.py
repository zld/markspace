"""Segment download/playback simulation over a network trace."""
from __future__ import annotations

import math
from bisect import bisect_right
from dataclasses import dataclass

from abr_engine.estimator import BandwidthEstimator
from abr_engine.models import (
    BufferState,
    ConfigError,
    DecisionRecord,
    Ladder,
    NetworkSample,
)
from abr_engine.policy import AbrPolicy, PolicyConfig


class TraceError(ValueError):
    """Raised when a network trace is malformed (message carries the index)."""


class SimulationError(RuntimeError):
    """Raised when a simulation cannot finish (e.g. download never ends)."""


class Trace:
    """Validated piecewise-constant bandwidth function of time."""

    def __init__(self, samples: list[NetworkSample]):
        if not samples:
            raise TraceError("trace contains no samples")
        times: list[float] = []
        effective: list[float | None] = []
        last_valid: float | None = None
        for i, s in enumerate(samples):
            if not math.isfinite(s.time) or s.time < 0.0:
                raise TraceError(f"sample {i}: time must be finite and >= 0")
            if times and s.time <= times[-1]:
                raise TraceError(
                    f"sample {i}: time {s.time} does not increase after {times[-1]}"
                )
            if s.bandwidth is not None:
                if not math.isfinite(s.bandwidth) or s.bandwidth < 0.0:
                    raise TraceError(
                        f"sample {i}: bandwidth must be finite and >= 0"
                    )
                last_valid = s.bandwidth
            times.append(s.time)
            effective.append(last_valid)
        first_valid = next((b for b in effective if b is not None), None)
        if first_valid is None:
            raise TraceError("trace has no valid bandwidth sample")
        # Missing observations hold the last known bandwidth; leading gaps
        # hold the first known one.
        self._eff = [first_valid if b is None else b for b in effective]
        if not any(b > 0.0 for b in self._eff):
            raise TraceError("trace has no positive bandwidth sample")
        self._times = times

    def download_time(self, bits: float, t_start: float) -> float:
        """Seconds needed to transfer ``bits`` starting at ``t_start``."""
        if bits <= 0.0:
            return 0.0
        times, eff = self._times, self._eff
        n = len(times)
        i = max(0, bisect_right(times, t_start) - 1)
        t = t_start
        remaining = bits
        while True:
            bw = eff[i]
            t_end = times[i + 1] if i + 1 < n else math.inf
            if bw > 0.0:
                available = bw * (t_end - t)
                if available >= remaining:
                    return t + remaining / bw - t_start
                remaining -= available
            if t_end == math.inf:
                raise SimulationError(
                    "trace ends at zero bandwidth; download cannot complete"
                )
            t = t_end
            i += 1


@dataclass(frozen=True)
class SimulationConfig:
    ladder: Ladder
    segment_count: int
    initial_buffer: float
    policy: PolicyConfig
    window: int = 8
    decay: float = 0.6
    max_buffer: float | None = None


def run_simulation(trace: Trace, config: SimulationConfig) -> list[DecisionRecord]:
    """Advance download and playback segment by segment; return decisions."""
    if not math.isfinite(config.initial_buffer) or config.initial_buffer < 0.0:
        raise ConfigError("initial buffer must be a finite number >= 0")
    if config.segment_count < 1:
        raise ConfigError("segment count must be >= 1")
    estimator = BandwidthEstimator(
        window=config.window,
        decay=config.decay,
        fallback_bandwidth=config.ladder.lowest * config.policy.safety,
    )
    policy = AbrPolicy(config.ladder, config.policy, estimator)
    buffer = BufferState(config.initial_buffer, config.max_buffer)
    records: list[DecisionRecord] = []
    now = 0.0
    for index in range(config.segment_count):
        decision = policy.select(buffer.seconds)
        bits = decision.bitrate * config.ladder.segment_seconds
        download = trace.download_time(bits, now)
        estimator.add_sample(bits, download)
        rebuffer = buffer.consume(download)
        buffer.add(config.ladder.segment_seconds)
        now += download
        records.append(DecisionRecord(
            index=index,
            selected_bitrate=decision.bitrate,
            estimated_bandwidth=decision.estimated_bandwidth,
            buffer_seconds=buffer.seconds,
            rebuffer_seconds=rebuffer,
            switch=decision.switch,
            fallback=decision.fallback,
            fallback_reason=decision.fallback_reason,
        ))
    return records


def trace_from_dict(data) -> tuple[Trace, dict]:
    """Parse a trace JSON document into a Trace plus metadata."""
    if not isinstance(data, dict) or not isinstance(data.get("samples"), list):
        raise TraceError("trace document must be an object with a 'samples' list")
    samples = []
    for i, item in enumerate(data["samples"]):
        if not isinstance(item, dict) or "time" not in item:
            raise TraceError(f"sample {i}: expected an object with a 'time' field")
        bandwidth = item.get("bandwidth")
        if bandwidth is not None and (
                isinstance(bandwidth, bool) or not isinstance(bandwidth, (int, float))):
            raise TraceError(f"sample {i}: bandwidth must be a number or null")
        time = item["time"]
        if isinstance(time, bool) or not isinstance(time, (int, float)):
            raise TraceError(f"sample {i}: time must be a number")
        samples.append(NetworkSample(float(time),
                                     None if bandwidth is None else float(bandwidth)))
    meta = {
        "segments": data.get("segments"),
        "segment_seconds": data.get("segment_seconds"),
        "seed": data.get("seed"),
    }
    return Trace(samples), meta
