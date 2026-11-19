"""Synthetic, reproducible network traces."""

from __future__ import annotations

import math
from dataclasses import dataclass

import numpy as np

from .errors import ConfigError

# Default profile: a stable link with periodic congestion dips and jitter.
DEFAULT_BASE_KBPS = 2400.0
DEFAULT_JITTER_KBPS = 500.0
DEFAULT_BASE_RTT_MS = 40.0
DEFAULT_LOSS_PROBABILITY = 0.02


@dataclass(frozen=True)
class NetworkSample:
    """What the network offered during one segment download."""

    throughput_kbps: float
    rtt_ms: float
    loss_probability: float
    loss_burst: bool


@dataclass(frozen=True)
class NetworkTrace:
    """A finished trace plus the parameters that produced it."""

    samples: tuple[NetworkSample, ...]
    seed: int
    base_kbps: float
    jitter_kbps: float
    base_rtt_ms: float
    loss_probability: float

    def __len__(self) -> int:
        return len(self.samples)

    def __iter__(self):
        return iter(self.samples)

    def __getitem__(self, index):
        return self.samples[index]


def _positive_finite(name: str, value: float) -> float:
    if not isinstance(value, (int, float)) or isinstance(value, bool):
        raise ConfigError(f"{name} must be a number, got {value!r}")
    if not math.isfinite(value) or value <= 0:
        raise ConfigError(f"{name} must be positive and finite, got {value!r}")
    return float(value)


def _probability(name: str, value: float) -> float:
    if not isinstance(value, (int, float)) or isinstance(value, bool):
        raise ConfigError(f"{name} must be a number, got {value!r}")
    if not math.isfinite(value) or not 0.0 <= value <= 1.0:
        raise ConfigError(f"{name} must be between 0 and 1, got {value!r}")
    return float(value)


def generate_trace(
    segments: int,
    seed: int,
    base_kbps: float = DEFAULT_BASE_KBPS,
    jitter_kbps: float = DEFAULT_JITTER_KBPS,
    base_rtt_ms: float = DEFAULT_BASE_RTT_MS,
    loss_probability: float = DEFAULT_LOSS_PROBABILITY,
) -> NetworkTrace:
    """Generate ``segments`` samples of bandwidth, RTT, and bursty loss.

    The same seed always yields the same trace. Bandwidth follows a base level
    plus a sine "congestion wave" and uniform jitter, clamped to stay positive
    and at least 50 kbps. A single seeded congestion episode (when there are
    enough segments) dims a contiguous window so the buffer can actually drain.
    RTT correlates with congestion, and loss arrives in short bursts whose
    onset probability is driven by ``loss_probability``.
    """
    if not isinstance(segments, int) or isinstance(segments, bool):
        raise ConfigError(f"segments must be an integer, got {segments!r}")
    if segments <= 0:
        raise ConfigError(f"segments must be at least 1, got {segments}")
    base_kbps = _positive_finite("base_kbps", base_kbps)
    jitter_kbps = _positive_finite("jitter_kbps", jitter_kbps)
    base_rtt_ms = _positive_finite("base_rtt_ms", base_rtt_ms)
    loss_probability = _probability("loss_probability", loss_probability)
    if not isinstance(seed, int) or isinstance(seed, bool):
        raise ConfigError(f"seed must be an integer, got {seed!r}")

    rng = np.random.default_rng(seed)
    phases = np.linspace(0.0, 4.0 * math.pi, segments, endpoint=False)
    wave = np.sin(phases) * 0.35 + np.sin(phases * 0.5 + 1.1) * 0.15
    noise = rng.uniform(-1.0, 1.0, segments)
    throughput = base_kbps * (1.0 + wave) + jitter_kbps * noise
    throughput = np.maximum(throughput, 50.0)

    # One sustained congestion episode: a smooth notch that removes most of the
    # link for a contiguous window, deep enough to drain a healthy buffer.
    congestion = np.ones(segments)
    if segments >= 8:
        span = int(rng.integers(8, min(16, segments) + 1))
        start = int(rng.integers(0, segments - span + 1))
        depth = float(rng.uniform(0.65, 0.9))
        taper = np.hanning(span + 2)[1:-1]
        congestion[start : start + span] = 1.0 - depth * taper
    throughput = np.maximum(throughput * congestion, 50.0)

    rtt_noise = rng.normal(0.0, 8.0, segments)
    load = np.clip((base_kbps - throughput) / max(base_kbps, 1.0), 0.0, None)
    rtt = base_rtt_ms * (1.0 + 1.5 * load) + rtt_noise
    rtt = np.maximum(rtt, 5.0)

    burst_starts = rng.random(segments) < loss_probability
    burst_lengths = rng.integers(1, 4, segments)
    samples: list[NetworkSample] = []
    remaining = 0
    for index in range(segments):
        if remaining > 0:
            remaining -= 1
            in_burst = True
        elif burst_starts[index]:
            remaining = int(burst_lengths[index]) - 1
            in_burst = True
        else:
            in_burst = False
        loss = 0.25 if in_burst else 0.0
        samples.append(
            NetworkSample(
                throughput_kbps=float(throughput[index]),
                rtt_ms=float(rtt[index]),
                loss_probability=loss,
                loss_burst=in_burst,
            )
        )
    return NetworkTrace(tuple(samples), seed, base_kbps, jitter_kbps, base_rtt_ms, loss_probability)
