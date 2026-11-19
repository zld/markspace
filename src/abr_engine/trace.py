"""Reproducible synthetic network traces: generation and JSON I/O.

A trace is a grid of evenly spaced samples starting at t=0. Each sample holds
bandwidth (bps), RTT (ms) and an outage flag; during outages bandwidth is 0.
Generation is seeded, so identical parameters yield byte-identical JSON.
"""
from __future__ import annotations

import json
from dataclasses import dataclass

import numpy as np

from .model import ConfigError, MAX_TRACE_SAMPLES

TRACE_VERSION = 1


@dataclass(frozen=True)
class TraceSample:
    t_s: float
    bandwidth_bps: float
    rtt_ms: float
    outage: bool


@dataclass(frozen=True)
class NetworkTrace:
    dt_s: float
    samples: tuple[TraceSample, ...]

    def __post_init__(self) -> None:
        if self.dt_s <= 0:
            raise ConfigError("trace sample interval must be positive")
        if not self.samples:
            raise ConfigError("trace must contain at least one sample")
        if len(self.samples) > MAX_TRACE_SAMPLES:
            raise ConfigError(
                f"trace has {len(self.samples)} samples, limit is {MAX_TRACE_SAMPLES}"
            )

    def sample_index_at(self, t_s: float) -> int:
        """Index of the sample covering time t (last sample persists)."""
        idx = int(max(t_s, 0.0) // self.dt_s)
        return min(idx, len(self.samples) - 1)


def generate_trace(
    seed: int,
    duration_s: float,
    dt_s: float = 0.5,
    base_bandwidth_bps: float = 3_000_000.0,
) -> NetworkTrace:
    """Build a deterministic trace with congestion drops, recovery and outages."""
    if duration_s <= 0:
        raise ConfigError("trace duration must be positive")
    if dt_s <= 0:
        raise ConfigError("trace sample interval must be positive")
    if base_bandwidth_bps <= 0:
        raise ConfigError("base bandwidth must be positive")
    rng = np.random.default_rng(seed)
    n = max(1, int(round(duration_s / dt_s)))
    if n > MAX_TRACE_SAMPLES:
        raise ConfigError(f"trace would have {n} samples, limit is {MAX_TRACE_SAMPLES}")

    bandwidth = np.full(n, base_bandwidth_bps)
    rtt = np.full(n, 35.0)

    # Congestion events: sudden step drops followed by gradual recovery.
    for _ in range(int(rng.integers(2, 6))):
        start = int(rng.integers(0, n))
        length = int(rng.integers(max(1, n // 20), max(2, n // 5)))
        depth = float(rng.uniform(0.15, 0.5))
        end = min(n, start + length)
        bandwidth[start:end] *= depth
        rtt[start:end] += float(rng.uniform(60.0, 180.0))
        rec = min(n - end, max(1, int(round(4.0 / dt_s))))
        if rec > 0:
            ramp = np.linspace(depth, 1.0, rec, endpoint=False)
            bandwidth[end:end + rec] *= ramp
            rtt[end:end + rec] += 80.0 * (1.0 - ramp)

    # Random jitter on top of the step profile.
    bandwidth *= rng.lognormal(mean=0.0, sigma=0.12, size=n)
    rtt += np.abs(rng.normal(0.0, 5.0, size=n))

    # Short disconnections: bandwidth forced to zero, RTT spikes.
    outage = np.zeros(n, dtype=bool)
    for _ in range(int(rng.integers(1, 4))):
        start = int(rng.integers(0, n))
        length = int(rng.integers(1, max(2, int(round(2.0 / dt_s)) + 1)))
        outage[start:start + length] = True
    bandwidth[outage] = 0.0
    rtt[outage] += 400.0

    samples = tuple(
        TraceSample(
            t_s=round(float(i) * dt_s, 6),
            bandwidth_bps=float(round(bandwidth[i])),
            rtt_ms=round(float(rtt[i]), 1),
            outage=bool(outage[i]),
        )
        for i in range(n)
    )
    return NetworkTrace(dt_s=dt_s, samples=samples)


def trace_to_json(trace: NetworkTrace) -> str:
    """Serialize deterministically: sorted keys, fixed layout, fixed rounding."""
    payload = {
        "version": TRACE_VERSION,
        "dt_s": trace.dt_s,
        "sample_count": len(trace.samples),
        "samples": [
            {
                "t_s": s.t_s,
                "bandwidth_bps": s.bandwidth_bps,
                "rtt_ms": s.rtt_ms,
                "outage": s.outage,
            }
            for s in trace.samples
        ],
    }
    return json.dumps(payload, indent=1, sort_keys=True) + "\n"


def trace_from_json(text: str) -> NetworkTrace:
    """Parse and validate a trace document; raise ConfigError on bad input."""
    try:
        payload = json.loads(text)
    except json.JSONDecodeError as exc:
        raise ConfigError(f"invalid trace JSON: {exc}") from exc
    if not isinstance(payload, dict):
        raise ConfigError("trace document must be a JSON object")
    if payload.get("version") != TRACE_VERSION:
        raise ConfigError(f"unsupported trace version: {payload.get('version')!r}")
    dt_s = payload.get("dt_s")
    raw_samples = payload.get("samples")
    if not isinstance(dt_s, (int, float)) or dt_s <= 0:
        raise ConfigError("trace dt_s must be a positive number")
    if not isinstance(raw_samples, list) or not raw_samples:
        raise ConfigError("trace must contain a non-empty samples list")
    if len(raw_samples) > MAX_TRACE_SAMPLES:
        raise ConfigError(
            f"trace has {len(raw_samples)} samples, limit is {MAX_TRACE_SAMPLES}"
        )
    samples = []
    prev_t = -1.0
    for i, raw in enumerate(raw_samples):
        try:
            sample = TraceSample(
                t_s=float(raw["t_s"]),
                bandwidth_bps=float(raw["bandwidth_bps"]),
                rtt_ms=float(raw["rtt_ms"]),
                outage=bool(raw["outage"]),
            )
        except (KeyError, TypeError, ValueError) as exc:
            raise ConfigError(f"malformed trace sample #{i}: {exc}") from exc
        if sample.t_s < 0 or sample.t_s <= prev_t and i > 0:
            raise ConfigError(f"trace sample #{i} has non-monotonic timestamp")
        if sample.bandwidth_bps < 0 or sample.rtt_ms < 0:
            raise ConfigError(f"trace sample #{i} has negative bandwidth or RTT")
        if sample.outage and sample.bandwidth_bps > 0:
            raise ConfigError(f"trace sample #{i} is an outage with non-zero bandwidth")
        prev_t = sample.t_s
        samples.append(sample)
    return NetworkTrace(dt_s=float(dt_s), samples=tuple(samples))
