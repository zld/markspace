"""Bandwidth estimation from per-download throughput samples."""

from __future__ import annotations

import math
from dataclasses import dataclass, field


@dataclass
class BandwidthEstimator:
    """Exponentially weighted throughput smoother with validity filtering.

    Each valid sample blends in with weight ``forget_factor``; older
    observations decay geometrically. Samples that are non-finite, non-positive,
    or outside the accepted range are ignored entirely so they cannot poison
    later estimates. ``available_kbps()`` applies a safety factor so the policy
    keeps headroom, and falls back to a conservative ``cold_start_kbps`` until
    the first valid sample arrives (never a division by zero).
    """

    forget_factor: float = 0.2
    safety_factor: float = 0.85
    sample_min_kbps: float = 1.0
    sample_max_kbps: float = 1_000_000.0
    cold_start_kbps: float = 300.0
    _estimate: float | None = field(default=None, init=False, repr=False)
    _count: int = field(default=0, init=False, repr=False)
    rejected_samples: int = field(default=0, init=False)

    def __post_init__(self) -> None:
        if not math.isfinite(self.forget_factor) or not 0.0 < self.forget_factor <= 1.0:
            raise ValueError(f"forget_factor must be in (0, 1], got {self.forget_factor!r}")
        if not math.isfinite(self.safety_factor) or not 0.0 < self.safety_factor <= 1.0:
            raise ValueError(f"safety_factor must be in (0, 1], got {self.safety_factor!r}")
        if not math.isfinite(self.sample_min_kbps) or self.sample_min_kbps <= 0:
            raise ValueError(f"sample_min_kbps must be positive, got {self.sample_min_kbps!r}")
        if not math.isfinite(self.sample_max_kbps) or self.sample_max_kbps < self.sample_min_kbps:
            raise ValueError(f"sample_max_kbps must be >= sample_min_kbps, got {self.sample_max_kbps!r}")
        if not math.isfinite(self.cold_start_kbps) or self.cold_start_kbps <= 0:
            raise ValueError(f"cold_start_kbps must be positive, got {self.cold_start_kbps!r}")

    @property
    def sample_count(self) -> int:
        return self._count

    def update(self, throughput_kbps: float) -> None:
        """Fold one throughput sample into the estimate.

        Non-finite, non-positive, and out-of-range samples are rejected and
        counted; they never change the estimate or the effective weight.
        """
        try:
            value = float(throughput_kbps)
        except (TypeError, ValueError):
            self.rejected_samples += 1
            return
        if not math.isfinite(value) or not self.sample_min_kbps <= value <= self.sample_max_kbps:
            self.rejected_samples += 1
            return
        if self._estimate is None:
            self._estimate = value
        else:
            blend = 1.0 - self.forget_factor
            self._estimate = blend * self._estimate + self.forget_factor * value
        self._count += 1

    def available_kbps(self) -> float:
        """Safety-discounted estimate, or the cold-start assumption before data."""
        if self._estimate is None:
            return self.cold_start_kbps
        return max(self.sample_min_kbps, self._estimate * self.safety_factor)

    def raw_estimate_kbps(self) -> float | None:
        return self._estimate

    def reset(self) -> None:
        self._estimate = None
        self._count = 0
        self.rejected_samples = 0
