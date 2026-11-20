"""audiocut: local PCM WAV silence segmentation toolkit.

Slices long recordings into sounding clips separated by silence,
stores results in SQLite and exports them as JSONL. Purely local:
no network, no audio devices.
"""
from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass, field

__version__ = "0.1.0"

# Default detector parameters.
DEFAULT_FRAME_MS = 20.0
DEFAULT_HOP_MS = 10.0
DEFAULT_SILENCE_DBFS = -48.0
DEFAULT_NOISE_MARGIN_DB = 8.0
DEFAULT_MIN_SILENCE_MS = 180.0

# Hysteresis: a frame must exceed threshold + HYSTERESIS_DB to count as
# sounding again once the detector has entered the silent state.
HYSTERESIS_DB = 6.0

# Floor for window levels so digital zero maps to a finite dB value.
MIN_DB = -140.0

# Accepted audio container limits.
MIN_CHANNELS = 1
MAX_CHANNELS = 8
MIN_SAMPLE_RATE = 8000
MAX_SAMPLE_RATE = 192000
PCM_SAMPLE_WIDTHS = (1, 2, 3, 4)  # bytes: 8/16/24/32-bit integer PCM

# Valid dBFS threshold range accepted from the command line.
MIN_THRESHOLD_DBFS = -120.0
MAX_THRESHOLD_DBFS = 0.0


class ParamsError(ValueError):
    """Raised when detector or batch parameters are invalid (exit code 2)."""


@dataclass(frozen=True)
class DetectorParams:
    """Tunable parameters of the silence detector."""

    frame_ms: float = DEFAULT_FRAME_MS
    hop_ms: float = DEFAULT_HOP_MS
    silence_dbfs: float = DEFAULT_SILENCE_DBFS
    noise_margin_db: float = DEFAULT_NOISE_MARGIN_DB
    min_silence_ms: float = DEFAULT_MIN_SILENCE_MS

    def validate(self) -> "DetectorParams":
        """Raise ParamsError on non-positive or out-of-range combinations."""
        p = self
        for name in ("frame_ms", "hop_ms", "min_silence_ms"):
            value = getattr(p, name)
            if not (value == value) or value <= 0:  # NaN or non-positive
                raise ParamsError(f"{name} must be a positive number, got {value!r}")
        if p.frame_ms < p.hop_ms:
            raise ParamsError(
                f"frame_ms ({p.frame_ms}) must be >= hop_ms ({p.hop_ms})"
            )
        if not (MIN_THRESHOLD_DBFS <= p.silence_dbfs <= MAX_THRESHOLD_DBFS):
            raise ParamsError(
                f"silence_dbfs must be within "
                f"[{MIN_THRESHOLD_DBFS}, {MAX_THRESHOLD_DBFS}], got {p.silence_dbfs!r}"
            )
        if not (p.noise_margin_db == p.noise_margin_db) or p.noise_margin_db < 0:
            raise ParamsError(
                f"noise_margin_db must be a non-negative number, "
                f"got {p.noise_margin_db!r}"
            )
        return p

    def summary_dict(self) -> dict:
        """Canonical parameter summary used for run deduplication."""
        return {
            "frame_ms": self.frame_ms,
            "hop_ms": self.hop_ms,
            "silence_dbfs": self.silence_dbfs,
            "noise_margin_db": self.noise_margin_db,
            "min_silence_ms": self.min_silence_ms,
        }

    def summary_json(self) -> str:
        return json.dumps(self.summary_dict(), sort_keys=True)

    def digest(self) -> str:
        """Stable content hash of the parameter summary."""
        return hashlib.sha256(self.summary_json().encode("utf-8")).hexdigest()


@dataclass(frozen=True)
class Segment:
    """Half-open sample interval [start_sample, end_sample)."""

    start_sample: int
    end_sample: int

    @property
    def length(self) -> int:
        return self.end_sample - self.start_sample


@dataclass
class DetectionResult:
    """Outcome of running the detector on one file."""

    sample_rate: int
    channels: int
    sample_frames: int
    segments: list = field(default_factory=list)  # list[Segment]
    silence: list = field(default_factory=list)  # list[Segment]
    threshold_dbfs: float = DEFAULT_SILENCE_DBFS  # effective, noise-adapted

    @property
    def clip_count(self) -> int:
        return len(self.segments)

    @property
    def silence_frames(self) -> int:
        return sum(s.length for s in self.silence)

    @property
    def silence_total_seconds(self) -> float:
        if self.sample_rate <= 0:
            return 0.0
        return self.silence_frames / self.sample_rate
