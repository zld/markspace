"""Throughput estimation: sliding window with exponential decay weights."""
from __future__ import annotations

import math
from collections import deque


class BandwidthEstimator:
    """Estimates available bandwidth from download observations.

    Keeps the last ``window`` throughput samples and combines them with
    exponential decay weights (newest sample weighs most) using a weighted
    harmonic mean, which is conservative for throughput. Zero-duration or
    non-finite observations are ignored. When no valid sample exists, a
    configured conservative fallback value is returned and the reason is
    recorded.
    """

    FALLBACK_REASON = "no_valid_samples"

    def __init__(self, window: int = 8, decay: float = 0.6,
                 fallback_bandwidth: float = 200_000.0):
        if window < 1:
            raise ValueError("estimator window must be >= 1")
        if not 0.0 < decay <= 1.0:
            raise ValueError("estimator decay must be in (0, 1]")
        if not math.isfinite(fallback_bandwidth) or fallback_bandwidth <= 0:
            raise ValueError("fallback bandwidth must be a positive finite number")
        self.window = window
        self.decay = decay
        self.fallback_bandwidth = float(fallback_bandwidth)
        self._samples: deque[float] = deque(maxlen=window)
        self.fallback_count = 0
        self.used_fallback = False
        self.last_fallback_reason: str | None = None

    def add_sample(self, bits: float, seconds: float) -> None:
        """Record one download observation; zero/invalid durations are ignored."""
        if (not math.isfinite(bits) or not math.isfinite(seconds)
                or seconds <= 0.0 or bits <= 0.0):
            return
        self._samples.append(bits / seconds)

    def estimate(self) -> float:
        """Return the current bandwidth estimate in bits per second."""
        valid = [s for s in self._samples if math.isfinite(s) and s > 0.0]
        if not valid:
            self.fallback_count += 1
            self.used_fallback = True
            self.last_fallback_reason = self.FALLBACK_REASON
            return self.fallback_bandwidth
        self.used_fallback = False
        self.last_fallback_reason = None
        n = len(valid)
        weights = [self.decay ** (n - 1 - i) for i in range(n)]
        weight_sum = sum(weights)
        denom = sum(w / s for w, s in zip(weights, valid))
        return weight_sum / denom
