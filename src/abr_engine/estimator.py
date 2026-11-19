"""Sliding-window median + exponential smoothing throughput estimator."""
from __future__ import annotations

from dataclasses import dataclass

import numpy as np

from .models import NetworkSample, ValidationError, require_finite


@dataclass(frozen=True)
class Estimate:
    """Throughput estimate at a point in time."""

    bandwidth_bps: float
    confidence: float  # 0.0 (no trust) .. 1.0 (full trust)
    sample_count: int


class ThroughputEstimator:
    """Combines a sliding-window median with an EWMA of past samples.

    Only samples at or before the requested time are used. Too few
    samples yield a conservative estimate; long gaps between samples
    lower the reported confidence. Timestamp regression is rejected.
    """

    def __init__(
        self,
        window_size: int = 8,
        alpha: float = 0.35,
        min_samples: int = 3,
        gap_threshold_s: float = 4.0,
        default_bandwidth_bps: float = 500_000.0,
    ) -> None:
        if isinstance(window_size, bool) or not isinstance(window_size, int) or window_size < 1:
            raise ValidationError(f"window_size must be a positive integer, got {window_size!r}")
        alpha = require_finite("alpha", alpha)
        if not 0.0 < alpha <= 1.0:
            raise ValidationError(f"alpha must be in (0, 1], got {alpha}")
        if isinstance(min_samples, bool) or not isinstance(min_samples, int) or min_samples < 1:
            raise ValidationError(f"min_samples must be a positive integer, got {min_samples!r}")
        self.window_size = window_size
        self.alpha = alpha
        self.min_samples = min_samples
        self.gap_threshold_s = require_finite("gap_threshold_s", gap_threshold_s)
        if self.gap_threshold_s <= 0.0:
            raise ValidationError("gap_threshold_s must be > 0")
        self.default_bandwidth_bps = require_finite("default_bandwidth_bps", default_bandwidth_bps)
        if self.default_bandwidth_bps <= 0.0:
            raise ValidationError("default_bandwidth_bps must be > 0")
        self._samples: list[NetworkSample] = []
        self._last_ts: float | None = None
        self._penalty = 0.0

    def add_sample(self, sample: NetworkSample) -> None:
        if self._last_ts is not None:
            if sample.timestamp_s < self._last_ts:
                raise ValidationError(
                    f"timestamp regression: {sample.timestamp_s} < {self._last_ts}"
                )
            if sample.timestamp_s - self._last_ts > self.gap_threshold_s:
                self._penalty = min(0.8, self._penalty + 0.5)
            else:
                self._penalty *= 0.5
        self._samples.append(sample)
        self._last_ts = sample.timestamp_s

    def estimate(self, now_s: float) -> Estimate:
        now_s = require_finite("now_s", now_s)
        usable = [s for s in self._samples if s.timestamp_s <= now_s]
        count = len(usable)
        if count == 0:
            return Estimate(self.default_bandwidth_bps, 0.1, 0)

        window = usable[-self.window_size :]
        median = float(np.median([s.bandwidth_bps for s in window]))
        ewma = usable[0].bandwidth_bps
        for s in usable[1:]:
            ewma = self.alpha * s.bandwidth_bps + (1.0 - self.alpha) * ewma
        combined = 0.5 * median + 0.5 * ewma
        if count < self.min_samples:
            # Conservative initialization: distrust the average while the
            # window is filling and anchor to the worst observation.
            combined = min(combined, min(s.bandwidth_bps for s in usable) * 0.8)

        count_factor = min(1.0, count / self.min_samples)
        recency = now_s - usable[-1].timestamp_s
        recency_factor = 1.0 if recency <= self.gap_threshold_s else 0.5
        confidence = count_factor * (1.0 - self._penalty) * recency_factor
        return Estimate(combined, round(confidence, 6), count)
