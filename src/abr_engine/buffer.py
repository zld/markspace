"""Playback buffer accounting for one segment at a time."""

from __future__ import annotations

import math
from dataclasses import dataclass

from .errors import ConfigError


@dataclass(frozen=True)
class BufferState:
    """Buffer occupancy observed around a single segment download."""

    before_seconds: float
    after_seconds: float
    stall_seconds: float
    download_seconds: float


class BufferModel:
    """Tracks buffer seconds across segment downloads.

    Playback consumes buffer while a segment downloads. Download time is the
    segment size divided by throughput plus the request RTT. If the buffer
    empties before the download finishes, the uncovered time is a stall and is
    accumulated. Once the download completes the segment is appended, capped at
    the configured capacity.
    """

    def __init__(self, capacity_seconds: float, initial_seconds: float = 0.0) -> None:
        if not math.isfinite(capacity_seconds) or capacity_seconds <= 0:
            raise ConfigError(f"buffer capacity must be positive, got {capacity_seconds!r}")
        if not math.isfinite(initial_seconds) or initial_seconds < 0:
            raise ConfigError(f"initial buffer must be non-negative, got {initial_seconds!r}")
        if initial_seconds > capacity_seconds:
            raise ConfigError(
                f"initial buffer {initial_seconds:g}s exceeds capacity {capacity_seconds:g}s"
            )
        self.capacity_seconds = float(capacity_seconds)
        self.level_seconds = float(initial_seconds)
        self.total_stall_seconds = 0.0
        self.segments_played = 0

    def download_time(
        self, bitrate_kbps: float, segment_seconds: float, throughput_kbps: float, rtt_ms: float
    ) -> float:
        """Seconds needed to download one segment at ``throughput_kbps``."""
        if not math.isfinite(bitrate_kbps) or bitrate_kbps <= 0:
            raise ConfigError(f"bitrate must be positive, got {bitrate_kbps!r}")
        if not math.isfinite(segment_seconds) or segment_seconds <= 0:
            raise ConfigError(f"segment duration must be positive, got {segment_seconds!r}")
        if not math.isfinite(throughput_kbps) or throughput_kbps <= 0:
            raise ConfigError(f"throughput must be positive, got {throughput_kbps!r}")
        if not math.isfinite(rtt_ms) or rtt_ms < 0:
            raise ConfigError(f"rtt must be non-negative, got {rtt_ms!r}")
        transfer = bitrate_kbps * segment_seconds / throughput_kbps
        return transfer + rtt_ms / 1000.0

    def consume(
        self,
        bitrate_kbps: float,
        segment_seconds: float,
        throughput_kbps: float,
        rtt_ms: float = 0.0,
    ) -> BufferState:
        """Download one segment, account for stall, then append it to the buffer."""
        download_seconds = self.download_time(
            bitrate_kbps, segment_seconds, throughput_kbps, rtt_ms
        )
        before = self.level_seconds
        remaining = before - download_seconds
        stall = max(0.0, -remaining)
        remaining = max(0.0, remaining)
        after = min(self.capacity_seconds, remaining + segment_seconds)
        self.level_seconds = after
        self.total_stall_seconds += stall
        self.segments_played += 1
        return BufferState(
            before_seconds=before,
            after_seconds=after,
            stall_seconds=stall,
            download_seconds=download_seconds,
        )

    def reset(self) -> None:
        self.level_seconds = 0.0
        self.total_stall_seconds = 0.0
        self.segments_played = 0
