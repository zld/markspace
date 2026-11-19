"""Buffer-aware ABR policy with upgrade hysteresis."""
from __future__ import annotations

from dataclasses import dataclass

from .estimator import Estimate
from .models import ValidationError, require_finite, validate_ladder


@dataclass(frozen=True)
class PolicyConfig:
    """Tunables for AbrPolicy.

    safety_factor:    fraction of the estimate trusted as usable throughput
    up_headroom:      estimate must exceed the next rung by this factor
    up_consecutive:   consecutive segments the up condition must hold
    dwell_s:          minimum seconds between the last switch and an upgrade
    rtt_reference_ms: RTT at which the RTT penalty factor reaches 0.5
    loss_sensitivity: weight of loss rate in the throughput penalty
    buffer_panic_s:   below this buffer, force an immediate downgrade
    buffer_block_up_s: below this buffer, upgrades are blocked
    """

    safety_factor: float = 0.85
    up_headroom: float = 1.25
    up_consecutive: int = 3
    dwell_s: float = 8.0
    rtt_reference_ms: float = 100.0
    loss_sensitivity: float = 4.0
    buffer_panic_s: float = 2.0
    buffer_block_up_s: float = 4.0

    def __post_init__(self) -> None:
        safety = require_finite("safety_factor", self.safety_factor)
        if not 0.0 < safety <= 1.0:
            raise ValidationError(f"safety_factor must be in (0, 1], got {safety}")
        headroom = require_finite("up_headroom", self.up_headroom)
        if headroom < 1.0:
            raise ValidationError(f"up_headroom must be >= 1, got {headroom}")
        if (
            isinstance(self.up_consecutive, bool)
            or not isinstance(self.up_consecutive, int)
            or self.up_consecutive < 1
        ):
            raise ValidationError(f"up_consecutive must be a positive integer, got {self.up_consecutive!r}")
        for name in ("dwell_s", "rtt_reference_ms", "loss_sensitivity",
                     "buffer_panic_s", "buffer_block_up_s"):
            value = require_finite(name, getattr(self, name))
            if value < 0.0:
                raise ValidationError(f"{name} must be >= 0, got {value}")
        if self.rtt_reference_ms <= 0.0:
            raise ValidationError("rtt_reference_ms must be > 0")


@dataclass(frozen=True)
class Decision:
    level: int
    previous_level: int
    reason: str

    @property
    def switch(self) -> str:
        if self.level > self.previous_level:
            return "up"
        if self.level < self.previous_level:
            return "down"
        return "none"


class AbrPolicy:
    """Rate-based ABR with hysteresis.

    Downgrades apply immediately; upgrades require the headroom condition
    to hold for `up_consecutive` consecutive segments and a dwell time
    since the last switch. A nearly empty buffer forces a downgrade.
    """

    def __init__(self, bitrates, config: PolicyConfig | None = None) -> None:
        self.ladder = validate_ladder(bitrates)
        self.config = config or PolicyConfig()
        self._level = 0
        self._up_streak = 0
        self._last_switch_s: float | None = None

    @property
    def level(self) -> int:
        return self._level

    def _effective_bandwidth(self, estimate: Estimate, rtt_ms: float, loss_rate: float) -> float:
        cfg = self.config
        loss_factor = max(0.0, 1.0 - loss_rate * cfg.loss_sensitivity)
        rtt_factor = cfg.rtt_reference_ms / (cfg.rtt_reference_ms + max(0.0, rtt_ms))
        confidence_factor = 0.5 + 0.5 * estimate.confidence
        return estimate.bandwidth_bps * cfg.safety_factor * loss_factor * rtt_factor * confidence_factor

    def select(
        self,
        *,
        now_s: float,
        estimate: Estimate,
        rtt_ms: float,
        loss_rate: float,
        buffer_s: float,
    ) -> Decision:
        now_s = require_finite("now_s", now_s)
        rtt_ms = require_finite("rtt_ms", rtt_ms)
        loss_rate = require_finite("loss_rate", loss_rate)
        buffer_s = require_finite("buffer_s", buffer_s)
        cfg = self.config
        effective = self._effective_bandwidth(estimate, rtt_ms, loss_rate)

        sustainable = 0
        for i, bitrate in enumerate(self.ladder):
            if bitrate <= effective:
                sustainable = i

        previous = self._level
        new_level = previous
        reason = "hold"
        if buffer_s < cfg.buffer_panic_s and previous > 0:
            new_level = max(0, min(sustainable, previous - 1))
            reason = "buffer-panic downgrade"
        elif sustainable < previous:
            new_level = sustainable
            reason = "immediate downgrade"
        elif sustainable > previous:
            nxt = previous + 1
            headroom_ok = self.ladder[nxt] * cfg.up_headroom <= effective
            buffer_ok = buffer_s >= cfg.buffer_block_up_s
            if headroom_ok and buffer_ok:
                self._up_streak += 1
                dwell_ok = (
                    self._last_switch_s is None
                    or now_s - self._last_switch_s >= cfg.dwell_s
                )
                if self._up_streak >= cfg.up_consecutive and dwell_ok:
                    new_level = nxt
                    reason = "hysteresis upgrade"
                else:
                    reason = "upgrade pending"
            else:
                self._up_streak = 0
        else:
            self._up_streak = 0

        if new_level != previous:
            self._last_switch_s = now_s
            self._up_streak = 0
        self._level = new_level
        return Decision(new_level, previous, reason)
