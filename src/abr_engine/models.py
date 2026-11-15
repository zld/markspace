"""Core data models: traces, ladder config, buffer state, decision records."""
from __future__ import annotations

import math
from dataclasses import dataclass


class ConfigError(ValueError):
    """Raised when engine configuration (ladder, thresholds) is invalid."""


@dataclass(frozen=True)
class NetworkSample:
    """One bandwidth observation in a network trace.

    ``bandwidth`` is bits per second; ``None`` marks a missing observation.
    """

    time: float
    bandwidth: float | None


@dataclass(frozen=True)
class Ladder:
    """Validated bitrate ladder plus the segment duration it must satisfy."""

    bitrates: tuple[int, ...]
    segment_seconds: float

    @classmethod
    def build(cls, bitrates, segment_seconds) -> "Ladder":
        if not bitrates:
            raise ConfigError("bitrate ladder must not be empty")
        vals: list[int] = []
        for raw in bitrates:
            if isinstance(raw, bool) or not isinstance(raw, (int, float)):
                raise ConfigError(f"bitrate {raw!r} is not a number")
            if not math.isfinite(raw) or raw <= 0:
                raise ConfigError(f"bitrate {raw!r} must be a positive finite number")
            vals.append(int(raw))
        if len(set(vals)) != len(vals):
            raise ConfigError("bitrate ladder contains duplicate rungs")
        if not math.isfinite(segment_seconds) or segment_seconds <= 0:
            raise ConfigError(
                "segment duration must be a positive finite number of seconds"
            )
        return cls(tuple(sorted(vals)), float(segment_seconds))

    @property
    def lowest(self) -> int:
        return self.bitrates[0]

    @property
    def highest(self) -> int:
        return self.bitrates[-1]


@dataclass
class BufferState:
    """Playback buffer in seconds; never goes negative."""

    seconds: float
    capacity: float | None = None

    def consume(self, dt: float) -> float:
        """Drain up to ``dt`` seconds; returns the shortfall (rebuffer time)."""
        shortfall = max(0.0, dt - self.seconds)
        self.seconds = max(0.0, self.seconds - dt)
        return shortfall

    def add(self, amount: float) -> None:
        self.seconds += amount
        if self.capacity is not None:
            self.seconds = min(self.seconds, self.capacity)


@dataclass(frozen=True)
class DecisionRecord:
    """Per-segment outcome logged by the simulator."""

    index: int
    selected_bitrate: int
    estimated_bandwidth: float
    buffer_seconds: float
    rebuffer_seconds: float
    switch: str  # "up" | "down" | "none"
    fallback: bool
    fallback_reason: str | None = None

    def to_dict(self) -> dict:
        return {
            "index": self.index,
            "selected_bitrate": self.selected_bitrate,
            "estimated_bandwidth": round(float(self.estimated_bandwidth), 3),
            "buffer_seconds": round(float(self.buffer_seconds), 6),
            "rebuffer_seconds": round(float(self.rebuffer_seconds), 6),
            "switch": self.switch,
            "fallback": self.fallback,
            "fallback_reason": self.fallback_reason,
        }
