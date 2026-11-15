"""Bitrate selection policy: safety margin, hysteresis, buffer protection."""
from __future__ import annotations

from dataclasses import dataclass

from abr_engine.estimator import BandwidthEstimator
from abr_engine.models import ConfigError, Ladder


@dataclass(frozen=True)
class PolicyConfig:
    """Tunables for the ABR policy.

    ``safety`` scales the bandwidth estimate into a sustainable ceiling.
    Below ``danger_buffer`` seconds the policy is forced down a rung;
    upswitches are only permitted at or above ``recover_buffer`` seconds,
    which keeps the selection from oscillating around a single threshold.
    """

    safety: float = 0.85
    danger_buffer: float = 4.0
    recover_buffer: float = 10.0

    def validate(self) -> None:
        if not 0.0 < self.safety <= 1.0:
            raise ConfigError("safety margin must be in (0, 1]")
        if self.danger_buffer < 0.0:
            raise ConfigError("danger buffer threshold must be >= 0")
        if self.recover_buffer <= self.danger_buffer:
            raise ConfigError(
                "recover buffer threshold must exceed the danger threshold"
            )


@dataclass(frozen=True)
class Decision:
    bitrate: int
    switch: str  # "up" | "down" | "none"
    estimated_bandwidth: float
    fallback: bool
    fallback_reason: str | None


class AbrPolicy:
    """Picks a ladder rung per segment from estimate, buffer, and last rung."""

    def __init__(self, ladder: Ladder, config: PolicyConfig,
                 estimator: BandwidthEstimator):
        config.validate()
        self._ladder = ladder
        self._config = config
        self._estimator = estimator
        self._current = ladder.lowest

    @property
    def current(self) -> int:
        return self._current

    def select(self, buffer_seconds: float) -> Decision:
        estimate = self._estimator.estimate()
        rungs = self._ladder.bitrates
        ceiling = estimate * self._config.safety
        allowed = [b for b in rungs if b <= ceiling]
        ceiling_target = max(allowed) if allowed else rungs[0]

        current = self._current
        if buffer_seconds < self._config.danger_buffer and current > rungs[0]:
            # Buffer emergency: force at least one rung down.
            lower = max(b for b in rungs if b < current)
            target = min(ceiling_target, lower)
        elif ceiling_target < current:
            # Estimate no longer sustains the current rung: step down now.
            target = ceiling_target
        elif ceiling_target > current:
            # Upswitch only once the buffer has recovered.
            if buffer_seconds >= self._config.recover_buffer:
                target = ceiling_target
            else:
                target = current
        else:
            target = current

        if target > current:
            switch = "up"
        elif target < current:
            switch = "down"
        else:
            switch = "none"
        self._current = target
        return Decision(
            bitrate=target,
            switch=switch,
            estimated_bandwidth=estimate,
            fallback=self._estimator.used_fallback,
            fallback_reason=self._estimator.last_fallback_reason,
        )
