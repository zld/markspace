"""Bitrate ladder definitions and validation."""

from __future__ import annotations

import math
from dataclasses import dataclass

from .errors import ConfigError


def parse_bitrates(text: str) -> tuple[float, ...]:
    """Parse a comma-separated bitrate list such as ``300,750,1200``."""
    if text is None or not text.strip():
        raise ConfigError("bitrates must not be empty")
    values: list[float] = []
    for part in text.split(","):
        token = part.strip()
        if not token:
            raise ConfigError(f"invalid bitrate entry: {part!r}")
        try:
            values.append(float(token))
        except ValueError as exc:
            raise ConfigError(f"invalid bitrate entry: {part!r}") from exc
    return tuple(values)


@dataclass(frozen=True)
class Ladder:
    """A strictly increasing set of bitrates (kbps) and a segment duration."""

    bitrates_kbps: tuple[float, ...]
    segment_seconds: float

    def __post_init__(self) -> None:
        if not self.bitrates_kbps:
            raise ConfigError("bitrate ladder must contain at least one tier")
        previous: float | None = None
        for tier, bitrate in enumerate(self.bitrates_kbps):
            if not math.isfinite(bitrate):
                raise ConfigError(f"bitrate at tier {tier} must be finite")
            if bitrate <= 0:
                raise ConfigError(f"bitrate at tier {tier} must be positive, got {bitrate:g}")
            if previous is not None and bitrate <= previous:
                raise ConfigError(
                    "bitrate ladder must be strictly increasing: "
                    f"tier {tier - 1}={previous:g}, tier {tier}={bitrate:g}"
                )
            previous = bitrate
        if not math.isfinite(self.segment_seconds) or self.segment_seconds <= 0:
            raise ConfigError(
                f"segment seconds must be positive and finite, got {self.segment_seconds:g}"
            )

    def __len__(self) -> int:
        return len(self.bitrates_kbps)

    @property
    def lowest_kbps(self) -> float:
        return self.bitrates_kbps[0]

    @property
    def highest_kbps(self) -> float:
        return self.bitrates_kbps[-1]

    def bitrate_for(self, tier: int) -> float:
        """Return the bitrate for ``tier``; tiers outside the ladder are rejected."""
        if not 0 <= tier < len(self):
            raise ConfigError(f"tier {tier} is outside the ladder")
        return self.bitrates_kbps[tier]

    def affordable_tier(self, available_kbps: float) -> int:
        """Highest tier whose bitrate is <= ``available_kbps`` (0 if none fit).

        Equality is inclusive, so an estimate exactly equal to a tier bitrate
        makes that tier affordable.
        """
        if not math.isfinite(available_kbps):
            raise ConfigError("available bandwidth must be finite")
        selected = 0
        for tier, bitrate in enumerate(self.bitrates_kbps):
            if bitrate <= available_kbps:
                selected = tier
            else:
                break
        return selected
