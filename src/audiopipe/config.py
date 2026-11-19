"""Validated configuration shared by the CLI, batch runner and tests."""

from __future__ import annotations

import math
from dataclasses import dataclass

from .errors import MAX_CHANNELS, MAX_FRAMES, MAX_SAMPLE_RATE, UsageError

CHANNEL_MODES = ("preserve", "mono")
DEFAULT_BLOCK_SIZE = 4096


@dataclass(frozen=True)
class ResampleConfig:
    """Parameters for the polyphase windowed-sinc resampler."""

    zero_crossings: int = 32
    rolloff: float = 0.945

    def __post_init__(self) -> None:
        if not 4 <= self.zero_crossings <= 512:
            raise UsageError("zero_crossings must be within [4, 512]")
        if not 0.1 <= self.rolloff <= 1.0:
            raise UsageError("rolloff must be within [0.1, 1.0]")


@dataclass(frozen=True)
class Config:
    """One validated batch configuration."""

    target_rate: int
    channels: str = "preserve"
    target_lufs: float | None = None
    max_peak_dbfs: float | None = None
    block_size: int = DEFAULT_BLOCK_SIZE
    resample: ResampleConfig = ResampleConfig()

    def __post_init__(self) -> None:
        if not 1 <= self.target_rate <= MAX_SAMPLE_RATE:
            raise UsageError(
                f"target rate {self.target_rate} outside [1, {MAX_SAMPLE_RATE}]"
            )
        if self.channels not in CHANNEL_MODES:
            raise UsageError(
                f"channels must be one of {CHANNEL_MODES}, got {self.channels!r}"
            )
        if self.target_lufs is not None:
            if not math.isfinite(self.target_lufs):
                raise UsageError("target_lufs must be finite")
            if not -70.0 <= self.target_lufs <= 0.0:
                raise UsageError("target_lufs must be within [-70, 0]")
        if self.max_peak_dbfs is not None:
            if not math.isfinite(self.max_peak_dbfs):
                raise UsageError("max_peak_dbfs must be finite")
            if not -120.0 <= self.max_peak_dbfs <= 0.0:
                raise UsageError("max_peak_dbfs must be within [-120, 0]")
        if self.block_size < 1:
            raise UsageError("block_size must be >= 1")

    @property
    def normalise(self) -> bool:
        return self.target_lufs is not None


def check_input_limits(
    rate: int, channels: int, frames: int, nbytes: int, *, max_bytes: int
) -> None:
    """Reject inputs outside the configured safety envelope."""
    from .errors import InputError

    if rate <= 0:
        raise InputError(f"invalid sample rate {rate}", reason="bad_sample_rate")
    if rate > MAX_SAMPLE_RATE:
        raise InputError(
            f"sample rate {rate} exceeds limit {MAX_SAMPLE_RATE}",
            reason="sample_rate_limit",
        )
    if channels <= 0:
        raise InputError(f"invalid channel count {channels}", reason="bad_channels")
    if channels > MAX_CHANNELS:
        raise InputError(
            f"channel count {channels} exceeds limit {MAX_CHANNELS}",
            reason="channel_limit",
        )
    if frames > MAX_FRAMES:
        raise InputError(
            f"frame count {frames} exceeds limit {MAX_FRAMES}",
            reason="frame_limit",
        )
    if nbytes > max_bytes:
        raise InputError(
            f"file size {nbytes} exceeds limit {max_bytes}",
            reason="file_size_limit",
        )
