"""Shared error types, exit codes, and small validation helpers."""

from __future__ import annotations

EXIT_OK = 0
EXIT_FAILURE = 1
EXIT_NON_MONOTONIC = 2
EXIT_INTERRUPTED = 130

# Guard rails: keep scans bounded on a local laptop.
MAX_FRAMES = 600
MIN_FRAMES = 1
MAX_RESOLUTION_PIXELS = 1920 * 1080
MAX_SCAN_COMBINATIONS = 256
MAX_WORKERS = 8
MIN_WORKERS = 1
MAX_BITRATE_KBPS = 100_000
MIN_BITRATE_KBPS = 1
MAX_DIMENSION = 1920

ALLOWED_PRESETS = (
    "ultrafast",
    "superfast",
    "veryfast",
    "faster",
    "fast",
    "medium",
    "slow",
    "slower",
    "veryslow",
)


class LadderForgeError(Exception):
    """Base class for expected, user-facing failures."""

    exit_code = EXIT_FAILURE


class ConfigError(LadderForgeError):
    """Invalid command-line configuration or scan matrix."""

    exit_code = EXIT_FAILURE


class EncoderUnavailableError(LadderForgeError):
    """ffmpeg or ffprobe is missing or unable to run."""

    exit_code = EXIT_FAILURE


class TaskError(LadderForgeError):
    """A single scan task failed; the scan may continue."""

    exit_code = EXIT_FAILURE


class NonMonotonicLadderError(LadderForgeError):
    """A monotonic ladder could not be constructed."""

    exit_code = EXIT_NON_MONOTONIC


class StoreLockError(LadderForgeError):
    """SQLite stayed busy beyond the configured timeout."""

    exit_code = EXIT_FAILURE


def clamp_int(value: int, low: int, high: int) -> int:
    return max(low, min(high, value))
