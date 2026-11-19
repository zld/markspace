"""Buffer-aware bitrate selection with directional switching hysteresis."""

from __future__ import annotations

import math
from dataclasses import dataclass, field

from .errors import ConfigError
from .ladder import Ladder


@dataclass(frozen=True)
class PolicyConfig:
    """Tunables for :class:`AbrPolicy`.

    ``danger_buffer_seconds``: below this level the policy downgrades even if
    the estimate would still support the current tier.
    ``up_min_buffer_seconds``: an up-switch additionally requires at least this
    much buffer so the extra bitrate does not immediately drain it.
    ``up_safety_margin``: the next tier's bitrate must satisfy
    ``next_bitrate * up_safety_margin <= available_kbps``.
    ``min_dwell_segments``: segments at the current tier before any up-switch.
    ``up_streak_segments``: consecutive segments that must satisfy the margin
    before an up-switch.
    """

    danger_buffer_seconds: float = 4.0
    up_min_buffer_seconds: float = 8.0
    up_safety_margin: float = 1.15
    min_dwell_segments: int = 2
    up_streak_segments: int = 2

    def __post_init__(self) -> None:
        if not math.isfinite(self.danger_buffer_seconds) or self.danger_buffer_seconds < 0:
            raise ConfigError("danger_buffer_seconds must be non-negative and finite")
        if not math.isfinite(self.up_min_buffer_seconds) or self.up_min_buffer_seconds < 0:
            raise ConfigError("up_min_buffer_seconds must be non-negative and finite")
        if not math.isfinite(self.up_safety_margin) or self.up_safety_margin < 1.0:
            raise ConfigError("up_safety_margin must be at least 1.0")
        if self.min_dwell_segments < 0:
            raise ConfigError("min_dwell_segments must be non-negative")
        if self.up_streak_segments < 1:
            raise ConfigError("up_streak_segments must be at least 1")


@dataclass(frozen=True)
class Decision:
    """One policy outcome, including why it was made."""

    tier: int
    bitrate_kbps: float
    reason: str
    affordable_tier: int
    changed: bool


@dataclass
class AbrPolicy:
    """Select a ladder tier from available bandwidth and buffer level.

    Rules, evaluated in order:
    1. Buffer below ``danger_buffer_seconds``: force a downgrade (at least one
       tier when the current tier is above zero), ignoring up-hysteresis.
    2. Current bitrate above available bandwidth: drop immediately to the
       affordable tier; down-switches are never blocked by up-hysteresis.
    3. Up-switch candidate: requires the safety margin, sufficient buffer, the
       minimum dwell at the current tier, and ``up_streak_segments`` consecutive
       qualifying selections. Only one tier is climbed at a time.
    4. Otherwise hold.

    Comparisons are inclusive where a threshold is "met" (``>=``) and strict
    where a level is "dangerously low" (``<``), so equal thresholds produce a
    single deterministic outcome.
    """

    ladder: Ladder
    config: PolicyConfig = field(default_factory=PolicyConfig)
    initial_tier: int = 0
    current_tier: int = field(init=False, default=0)
    _dwell: int = field(init=False, default=0, repr=False)
    _up_streak: int = field(init=False, default=0, repr=False)

    def __post_init__(self) -> None:
        if not 0 <= self.initial_tier < len(self.ladder):
            raise ConfigError(
                f"initial tier {self.initial_tier} is outside the ladder of {len(self.ladder)} tiers"
            )
        self.current_tier = self.initial_tier

    @property
    def dwell_segments(self) -> int:
        return self._dwell

    @property
    def up_streak(self) -> int:
        return self._up_streak

    def select(self, available_kbps: float, buffer_seconds: float) -> Decision:
        if not math.isfinite(available_kbps) or available_kbps <= 0:
            raise ConfigError(f"available bandwidth must be positive, got {available_kbps!r}")
        if not math.isfinite(buffer_seconds) or buffer_seconds < 0:
            raise ConfigError(f"buffer level must be non-negative, got {buffer_seconds!r}")

        current = self.current_tier
        affordable = self.ladder.affordable_tier(available_kbps)
        reason = "hold"
        target = current

        if buffer_seconds < self.config.danger_buffer_seconds:
            target = min(current, affordable)
            if target == current and current > 0:
                target = current - 1
            reason = "buffer-danger"
            self._up_streak = 0
        elif self.ladder.bitrate_for(current) > available_kbps:
            target = affordable
            reason = "down-bandwidth"
            self._up_streak = 0
        elif affordable > current:
            next_tier = current + 1
            next_bitrate = self.ladder.bitrate_for(next_tier)
            margin_met = next_bitrate * self.config.up_safety_margin <= available_kbps
            buffer_ok = buffer_seconds >= self.config.up_min_buffer_seconds
            if margin_met and buffer_ok:
                self._up_streak += 1
            else:
                self._up_streak = 0
            if self._up_streak >= self.config.up_streak_segments and self._dwell >= self.config.min_dwell_segments:
                target = next_tier
                reason = "up"
                self._up_streak = 0
        else:
            self._up_streak = 0

        changed = target != current
        if changed:
            self.current_tier = target
            self._dwell = 0
        else:
            self._dwell += 1
        return Decision(
            tier=target,
            bitrate_kbps=self.ladder.bitrate_for(target),
            reason=reason,
            affordable_tier=affordable,
            changed=changed,
        )
