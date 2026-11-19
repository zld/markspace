"""Core data model: bitrate ladder, stream config, buffer state, decisions."""
from __future__ import annotations

from dataclasses import dataclass

MAX_SEGMENTS = 100_000
MAX_TRACE_SAMPLES = 10_000_000


class ConfigError(ValueError):
    """Invalid user-supplied configuration or input (CLI exit code 2)."""


@dataclass(frozen=True)
class BitrateLadder:
    """Strictly increasing set of available bitrates (bits per second)."""

    levels_bps: tuple[int, ...]

    def __post_init__(self) -> None:
        levels = tuple(self.levels_bps)
        if len(levels) < 4:
            raise ConfigError("bitrate ladder needs at least 4 levels")
        if any(b <= 0 for b in levels):
            raise ConfigError("bitrate levels must be positive integers")
        if len(set(levels)) != len(levels):
            raise ConfigError("bitrate levels must not contain duplicates")
        if any(hi <= lo for lo, hi in zip(levels, levels[1:])):
            raise ConfigError("bitrate levels must be strictly increasing")
        object.__setattr__(self, "levels_bps", levels)

    @property
    def lowest_bps(self) -> int:
        return self.levels_bps[0]

    def highest_affordable(self, budget_bps: float) -> int:
        """Index of the highest level at or below the budget (0 if none fits)."""
        idx = 0
        for i, level in enumerate(self.levels_bps):
            if level <= budget_bps:
                idx = i
        return idx


@dataclass(frozen=True)
class StreamConfig:
    """Fixed stream layout and buffer limits for one simulation run."""

    segment_duration_s: float
    segment_count: int
    initial_buffer_s: float
    max_buffer_s: float

    def __post_init__(self) -> None:
        if self.segment_duration_s <= 0:
            raise ConfigError("segment duration must be positive")
        if not 1 <= self.segment_count <= MAX_SEGMENTS:
            raise ConfigError(f"segment count must be within [1, {MAX_SEGMENTS}]")
        if self.initial_buffer_s < 0:
            raise ConfigError("initial buffer must be non-negative")
        if self.max_buffer_s <= 0:
            raise ConfigError("max buffer must be positive")
        if self.initial_buffer_s > self.max_buffer_s:
            raise ConfigError("initial buffer must not exceed max buffer")


@dataclass
class BufferState:
    """Mutable playout buffer; stall time accumulates in simulated time."""

    level_s: float
    max_s: float
    stalled: bool = False
    total_rebuffer_s: float = 0.0

    def consume(self, elapsed_s: float) -> float:
        """Drain playback for elapsed_s; return stall seconds incurred.

        A boundary where the buffer reaches exactly zero as the interval ends
        counts no stall, so segment/trace boundaries never double-count.
        """
        if elapsed_s < 0:
            raise ValueError("elapsed time must be non-negative")
        if self.level_s <= 0.0:
            self.stalled = True
            self.total_rebuffer_s += elapsed_s
            return elapsed_s
        played = min(self.level_s, elapsed_s)
        self.level_s -= played
        stall = elapsed_s - played
        if stall > 0.0:
            self.stalled = True
            self.total_rebuffer_s += stall
        return stall

    def add_segment(self, duration_s: float) -> None:
        """Append a downloaded segment; playback resumes if it has content."""
        self.level_s = min(self.level_s + duration_s, self.max_s)
        if self.level_s > 0.0:
            self.stalled = False


@dataclass(frozen=True)
class DecisionRecord:
    """One per-segment decision and its simulated outcome."""

    segment: int
    bitrate_bps: int
    estimated_bps: float
    download_s: float
    buffer_s: float
    rebuffer_s: float
