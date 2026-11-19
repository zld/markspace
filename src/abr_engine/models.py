"""Core data structures and input validation for the ABR engine."""
from __future__ import annotations

import math
from dataclasses import dataclass


class ValidationError(ValueError):
    """Raised when user-supplied input fails validation."""


def require_finite(name: str, value: float) -> float:
    """Coerce value to float and require it to be finite."""
    try:
        result = float(value)
    except (TypeError, ValueError) as exc:
        raise ValidationError(f"{name} must be a number, got {value!r}") from exc
    if not math.isfinite(result):
        raise ValidationError(f"{name} must be finite, got {value!r}")
    return result


def require_positive_int(name: str, value: object) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise ValidationError(f"{name} must be an integer, got {value!r}")
    if value <= 0:
        raise ValidationError(f"{name} must be positive, got {value}")
    return value


@dataclass(frozen=True)
class NetworkSample:
    """One network observation: bandwidth, RTT and loss at a timestamp."""

    timestamp_s: float
    bandwidth_bps: float
    rtt_ms: float
    loss_rate: float

    def __post_init__(self) -> None:
        object.__setattr__(self, "timestamp_s", require_finite("timestamp_s", self.timestamp_s))
        object.__setattr__(self, "bandwidth_bps", require_finite("bandwidth_bps", self.bandwidth_bps))
        object.__setattr__(self, "rtt_ms", require_finite("rtt_ms", self.rtt_ms))
        object.__setattr__(self, "loss_rate", require_finite("loss_rate", self.loss_rate))
        if self.timestamp_s < 0.0:
            raise ValidationError(f"timestamp_s must be >= 0, got {self.timestamp_s}")
        if self.bandwidth_bps <= 0.0:
            raise ValidationError(f"bandwidth_bps must be > 0, got {self.bandwidth_bps}")
        if self.rtt_ms < 0.0:
            raise ValidationError(f"rtt_ms must be >= 0, got {self.rtt_ms}")
        if not 0.0 <= self.loss_rate <= 1.0:
            raise ValidationError(f"loss_rate must be in [0, 1], got {self.loss_rate}")


def validate_ladder(bitrates) -> tuple[int, ...]:
    """Require a non-empty, strictly increasing ladder of positive bitrates."""
    if not bitrates:
        raise ValidationError("bitrate ladder must not be empty")
    ladder = tuple(require_positive_int(f"bitrates[{i}]", b) for i, b in enumerate(bitrates))
    for i in range(1, len(ladder)):
        if ladder[i] <= ladder[i - 1]:
            raise ValidationError(
                "bitrate ladder must be strictly increasing: "
                f"bitrates[{i - 1}]={ladder[i - 1]} >= bitrates[{i}]={ladder[i]}"
            )
    return ladder


@dataclass(frozen=True)
class Segment:
    """A video segment: a rung of the ladder played for duration_s.

    size_bytes may be given explicitly for encoded content; when both the
    explicit size and the bitrate*duration derived size are present and
    disagree, the segment is rejected.
    """

    index: int
    bitrate_bps: int
    duration_s: float
    size_bytes: float | None = None

    SIZE_TOLERANCE = 0.01  # relative tolerance for explicit/derived conflicts

    def __post_init__(self) -> None:
        if isinstance(self.index, bool) or not isinstance(self.index, int) or self.index < 0:
            raise ValidationError(f"segment index must be a non-negative integer, got {self.index!r}")
        require_positive_int("bitrate_bps", self.bitrate_bps)
        duration = require_finite("duration_s", self.duration_s)
        if duration <= 0.0:
            raise ValidationError(f"segment duration_s must be > 0, got {duration}")
        object.__setattr__(self, "duration_s", duration)
        if self.size_bytes is not None:
            size = require_finite("size_bytes", self.size_bytes)
            if size <= 0.0:
                raise ValidationError(f"size_bytes must be > 0, got {size}")
            object.__setattr__(self, "size_bytes", size)
            computed = self.bitrate_bps * duration / 8.0
            if abs(size - computed) > max(1.0, self.SIZE_TOLERANCE * computed):
                raise ValidationError(
                    f"segment {self.index}: explicit size_bytes {size} conflicts "
                    f"with bitrate*duration/8 = {computed}"
                )

    def resolved_size_bytes(self) -> float:
        if self.size_bytes is not None:
            return self.size_bytes
        return self.bitrate_bps * self.duration_s / 8.0


@dataclass
class PlaybackState:
    """Mutable playback position shared across simulation steps."""

    time_s: float = 0.0
    buffer_s: float = 0.0
    level: int = 0


@dataclass(frozen=True)
class SegmentRecord:
    """Per-segment decision record emitted by the simulator."""

    index: int
    time_s: float
    estimated_bandwidth_bps: float
    confidence: float
    buffer_before_s: float
    buffer_after_s: float
    selected_bitrate_bps: int
    download_s: float
    switch: str  # "none" | "up" | "down"
    stall_s: float
