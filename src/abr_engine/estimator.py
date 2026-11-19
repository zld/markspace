"""Throughput estimation from completed segment downloads only."""
from __future__ import annotations

from collections import deque
from dataclasses import dataclass

from .model import ConfigError

ESTIMATOR_MODES = ("window", "ema")


@dataclass(frozen=True)
class EstimatorConfig:
    """mode: 'window' (sliding window) or 'ema' (exponential smoothing)."""

    mode: str = "window"
    window: int = 5
    alpha: float = 0.3

    def __post_init__(self) -> None:
        if self.mode not in ESTIMATOR_MODES:
            raise ConfigError(f"estimator mode must be one of {ESTIMATOR_MODES}")
        if self.window < 1:
            raise ConfigError("estimator window must be >= 1")
        if not 0.0 < self.alpha <= 1.0:
            raise ConfigError("estimator alpha must be within (0, 1]")


@dataclass(frozen=True)
class DownloadSample:
    """One completed download; invalid samples never reach the estimate."""

    bytes_transferred: int
    duration_s: float
    crossed_outage: bool = False

    @property
    def valid(self) -> bool:
        return (
            self.bytes_transferred > 0
            and self.duration_s > 0.0
            and not self.crossed_outage
        )


class ThroughputEstimator:
    """Estimates usable throughput; conservative until real samples arrive."""

    def __init__(self, config: EstimatorConfig, conservative_bps: float) -> None:
        if conservative_bps <= 0:
            raise ConfigError("conservative initial estimate must be positive")
        self._config = config
        self._conservative_bps = float(conservative_bps)
        self._samples: deque[tuple[int, float]] = deque(maxlen=config.window)
        self._ema_bps: float | None = None

    def add_sample(self, sample: DownloadSample) -> None:
        """Feed a completed download; zero-byte, non-positive-duration and
        outage-crossing samples are dropped so they cannot pollute estimates."""
        if not sample.valid:
            return
        if self._config.mode == "window":
            self._samples.append((sample.bytes_transferred, sample.duration_s))
        else:
            bps = 8.0 * sample.bytes_transferred / sample.duration_s
            if self._ema_bps is None:
                self._ema_bps = bps
            else:
                a = self._config.alpha
                self._ema_bps = a * bps + (1.0 - a) * self._ema_bps

    def estimate_bps(self) -> float:
        if self._config.mode == "window":
            if not self._samples:
                return self._conservative_bps
            bits = 8.0 * sum(b for b, _ in self._samples)
            duration = sum(d for _, d in self._samples)
            return bits / duration if duration > 0.0 else self._conservative_bps
        return self._ema_bps if self._ema_bps is not None else self._conservative_bps
