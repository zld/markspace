"""Data structures and sample-boundary rules for soundindex.

All intervals are half-open sample ranges [start_sample, end_sample),
clamped to [0, n_samples) of the source file.
"""
from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass


class ParamError(ValueError):
    """Invalid or contradictory detection parameters (exit code 2)."""


@dataclass(frozen=True)
class DetectParams:
    frame_ms: float = 20.0
    hop_ms: float = 10.0
    noise_margin_db: float = 8.0
    min_silence_ms: float = 120.0
    min_clip_ms: float = 80.0
    fade_guard_ms: float = 30.0
    hysteresis_db: float = 4.0

    def validate(self) -> "DetectParams":
        p = self
        if p.frame_ms <= 0:
            raise ParamError("frame-ms must be > 0")
        if p.hop_ms <= 0:
            raise ParamError("hop-ms must be > 0")
        if p.hop_ms > p.frame_ms:
            raise ParamError("hop-ms must not exceed frame-ms")
        if p.noise_margin_db <= 0:
            raise ParamError("noise-margin-db must be > 0")
        if p.hysteresis_db <= 0:
            raise ParamError("hysteresis-db must be > 0")
        if p.min_silence_ms < 0:
            raise ParamError("min-silence-ms must be >= 0")
        if p.min_clip_ms <= 0:
            raise ParamError("min-clip-ms must be > 0")
        if p.fade_guard_ms < 0:
            raise ParamError("fade-guard-ms must be >= 0")
        return self

    def as_dict(self) -> dict:
        return {
            "frame_ms": self.frame_ms,
            "hop_ms": self.hop_ms,
            "noise_margin_db": self.noise_margin_db,
            "min_silence_ms": self.min_silence_ms,
            "min_clip_ms": self.min_clip_ms,
            "fade_guard_ms": self.fade_guard_ms,
            "hysteresis_db": self.hysteresis_db,
        }

    def fingerprint(self) -> str:
        blob = json.dumps(self.as_dict(), sort_keys=True).encode("utf-8")
        return hashlib.sha256(blob).hexdigest()

    def ms_to_samples(self, ms: float, sample_rate: int) -> int:
        return int(round(ms * sample_rate / 1000.0))


@dataclass(frozen=True)
class Interval:
    """Half-open sample interval [start_sample, end_sample)."""

    start_sample: int
    end_sample: int

    def __post_init__(self) -> None:
        if self.start_sample < 0 or self.end_sample < self.start_sample:
            raise ValueError(f"invalid interval [{self.start_sample}, {self.end_sample})")

    @property
    def length(self) -> int:
        return self.end_sample - self.start_sample


@dataclass(frozen=True)
class FileInfo:
    path: str
    sample_rate: int
    channels: int
    bit_depth: int
    n_samples: int

    @property
    def duration_ms(self) -> float:
        if self.sample_rate <= 0:
            return 0.0
        return self.n_samples * 1000.0 / self.sample_rate


@dataclass
class DetectionResult:
    clips: list  # list[Interval], sorted, non-overlapping
    silences: list  # list[Interval], half-open complement of clips
    noise_floor_db: float
    threshold_db: float

    @property
    def silence_total_samples(self) -> int:
        return sum(s.length for s in self.silences)
