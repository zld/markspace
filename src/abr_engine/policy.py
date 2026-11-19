"""Bitrate selection: safety margin, buffer awareness, upswitch hysteresis."""
from __future__ import annotations

from dataclasses import dataclass

from .model import BitrateLadder, ConfigError


@dataclass(frozen=True)
class PolicyConfig:
    """Tuning knobs for the rate selection policy.

    safety_margin: fraction of the estimate treated as sustainable.
    hysteresis: consecutive estimates above the next level required to go up.
    buffer_safety_s: below this buffer level the budget shrinks further.
    low_buffer_factor: extra multiplier applied while the buffer is low.
    """

    safety_margin: float = 0.85
    hysteresis: int = 3
    buffer_safety_s: float = 4.0
    low_buffer_factor: float = 0.5

    def __post_init__(self) -> None:
        if not 0.0 < self.safety_margin <= 1.0:
            raise ConfigError("safety margin must be within (0, 1]")
        if self.hysteresis < 1:
            raise ConfigError("hysteresis must be >= 1")
        if self.buffer_safety_s < 0:
            raise ConfigError("buffer safety threshold must be non-negative")
        if not 0.0 < self.low_buffer_factor <= 1.0:
            raise ConfigError("low buffer factor must be within (0, 1]")


class RatePolicy:
    """Stateful per-stream policy: fast to switch down, slow to switch up."""

    def __init__(self, ladder: BitrateLadder, config: PolicyConfig) -> None:
        self._ladder = ladder
        self._config = config
        self._level = 0
        self._up_streak = 0

    @property
    def current_bitrate_bps(self) -> int:
        return self._ladder.levels_bps[self._level]

    def decide(self, estimated_bps: float, buffer_s: float) -> int:
        """Pick the bitrate for the next segment (bits per second)."""
        budget = estimated_bps * self._config.safety_margin
        if buffer_s < self._config.buffer_safety_s:
            budget *= self._config.low_buffer_factor
        target = self._ladder.highest_affordable(budget)
        if target > self._level:
            # Upswitch only after the estimate clears the bar repeatedly,
            # so a stale high estimate cannot trigger a premature jump.
            self._up_streak += 1
            if self._up_streak >= self._config.hysteresis:
                self._level = target
                self._up_streak = 0
        elif target < self._level:
            # Downswitch immediately: reacting late risks buffer underrun.
            self._level = target
            self._up_streak = 0
        else:
            self._up_streak = 0
        return self.current_bitrate_bps
