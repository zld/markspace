"""Deterministic network trace generation and JSON persistence."""
from __future__ import annotations

import json
import math
import os

import numpy as np

from .models import NetworkSample, ValidationError, require_finite

TRACE_VERSION = 1
MAX_TRACE_BYTES = 32 * 1024 * 1024

_HEALTHY_LOG_BW = math.log(4.0e6)


def generate_trace(seed: int, duration_s: float, interval_s: float) -> list[NetworkSample]:
    """Generate a reproducible bandwidth/RTT/loss trace.

    A mean-reverting log-bandwidth random walk with random throughput
    drops and short recoveries; RTT and loss correlate with low bandwidth.
    """
    if isinstance(seed, bool) or not isinstance(seed, int) or seed < 0:
        raise ValidationError(f"seed must be a non-negative integer, got {seed!r}")
    duration_s = require_finite("duration_s", duration_s)
    interval_s = require_finite("interval_s", interval_s)
    if duration_s <= 0.0:
        raise ValidationError(f"duration_s must be > 0, got {duration_s}")
    if interval_s <= 0.0:
        raise ValidationError(f"interval_s must be > 0, got {interval_s}")
    if interval_s > duration_s:
        raise ValidationError(
            f"interval_s {interval_s} exceeds duration_s {duration_s}; need at least 2 samples"
        )

    count = int(round(duration_s / interval_s)) + 1
    rng = np.random.default_rng(seed)
    log_bw = _HEALTHY_LOG_BW
    target = log_bw
    samples: list[NetworkSample] = []
    for i in range(count):
        u = rng.random()
        if u < 0.04:  # sudden throughput collapse
            target = log_bw - rng.uniform(0.8, 1.8)
        elif u < 0.10:  # short recovery toward a healthy level
            target = _HEALTHY_LOG_BW + rng.uniform(-0.3, 0.6)
        log_bw += 0.35 * (target - log_bw) + rng.normal(0.0, 0.08)
        bandwidth = float(np.clip(math.exp(log_bw), 1.0e5, 4.0e7))
        rtt = float(np.clip(60.0 - 30.0 * (bandwidth / 4.0e7) + rng.normal(0.0, 5.0), 5.0, 500.0))
        loss = 0.004 + (0.08 if bandwidth < 8.0e5 else 0.0) + abs(rng.normal(0.0, 0.002))
        samples.append(
            NetworkSample(
                timestamp_s=round(i * interval_s, 6),
                bandwidth_bps=round(bandwidth, 1),
                rtt_ms=round(rtt, 3),
                loss_rate=round(float(np.clip(loss, 0.0, 0.5)), 6),
            )
        )
    return samples


def save_trace(samples: list[NetworkSample], path: str) -> None:
    """Serialize samples to JSON, enforcing finite values and a size cap."""
    payload = {
        "version": TRACE_VERSION,
        "sample_count": len(samples),
        "samples": [
            {
                "timestamp_s": s.timestamp_s,
                "bandwidth_bps": s.bandwidth_bps,
                "rtt_ms": s.rtt_ms,
                "loss_rate": s.loss_rate,
            }
            for s in samples
        ],
    }
    try:
        text = json.dumps(payload, indent=1, allow_nan=False)
    except (ValueError, TypeError) as exc:
        raise ValidationError(f"trace contains non-finite values: {exc}") from exc
    size = len(text.encode("utf-8"))
    if size > MAX_TRACE_BYTES:
        raise ValidationError(f"trace JSON of {size} bytes exceeds limit {MAX_TRACE_BYTES}")
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(text)


def load_trace(path: str) -> list[NetworkSample]:
    """Load and validate a trace JSON file written by save_trace."""
    try:
        size = os.path.getsize(path)
    except OSError as exc:
        raise ValidationError(f"cannot read trace file {path}: {exc}") from exc
    if size > MAX_TRACE_BYTES:
        raise ValidationError(f"trace file {path} is {size} bytes, limit is {MAX_TRACE_BYTES}")
    try:
        with open(path, encoding="utf-8") as fh:
            payload = json.load(fh)
    except (OSError, json.JSONDecodeError) as exc:
        raise ValidationError(f"invalid trace file {path}: {exc}") from exc
    if not isinstance(payload, dict) or payload.get("version") != TRACE_VERSION:
        raise ValidationError(f"trace file {path}: unsupported or missing version")
    raw_samples = payload.get("samples")
    if not isinstance(raw_samples, list) or not raw_samples:
        raise ValidationError(f"trace file {path}: 'samples' must be a non-empty list")

    samples: list[NetworkSample] = []
    previous_ts = None
    for i, raw in enumerate(raw_samples):
        if not isinstance(raw, dict):
            raise ValidationError(f"trace file {path}: samples[{i}] must be an object")
        try:
            sample = NetworkSample(
                timestamp_s=raw["timestamp_s"],
                bandwidth_bps=raw["bandwidth_bps"],
                rtt_ms=raw["rtt_ms"],
                loss_rate=raw["loss_rate"],
            )
        except KeyError as exc:
            raise ValidationError(f"trace file {path}: samples[{i}] missing field {exc}") from exc
        except ValidationError as exc:
            raise ValidationError(f"trace file {path}: samples[{i}]: {exc}") from exc
        if previous_ts is not None and sample.timestamp_s < previous_ts:
            raise ValidationError(
                f"trace file {path}: samples[{i}] timestamp {sample.timestamp_s} "
                f"goes backwards after {previous_ts}"
            )
        previous_ts = sample.timestamp_s
        samples.append(sample)
    return samples
