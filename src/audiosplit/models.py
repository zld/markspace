"""Domain models: audio containers, detection parameters, segments and results."""
from __future__ import annotations

from dataclasses import dataclass, field
import numpy as np


class ParamError(ValueError):
    """Raised when a configurable parameter falls outside its allowed range."""


@dataclass(frozen=True)
class DetectParams:
    """All tunable detector parameters, in milliseconds / decibels.

    frame_ms        analysis frame length
    noise_margin_db SNR margin above the estimated noise floor
    min_silence_ms  silence shorter than this merges adjacent segments
    min_segment_ms  segments shorter than this are dropped (after merging)
    pad_ms          symmetric extension applied to final segment boundaries
    """

    frame_ms: float = 10.0
    noise_margin_db: float = 8.0
    min_silence_ms: float = 120.0
    min_segment_ms: float = 30.0
    pad_ms: float = 20.0

    # Inclusive allowed ranges; the CLI rejects anything outside them.
    LIMITS: dict[str, tuple[float, float]] = field(
        default_factory=lambda: {
            "frame_ms": (1.0, 100.0),
            "noise_margin_db": (0.0, 60.0),
            "min_silence_ms": (0.0, 60_000.0),
            "min_segment_ms": (0.0, 60_000.0),
            "pad_ms": (0.0, 60_000.0),
        },
        repr=False,
        compare=False,
    )

    def validate(self) -> None:
        for name, (low, high) in self.LIMITS.items():
            value = getattr(self, name)
            if not isinstance(value, (int, float)) or not np.isfinite(value):
                raise ParamError(f"{name} must be a finite number, got {value!r}")
            if not low <= float(value) <= high:
                raise ParamError(
                    f"{name}={value} out of allowed range [{low}, {high}]"
                )

    def as_dict(self) -> dict[str, float]:
        return {
            "frame_ms": self.frame_ms,
            "noise_margin_db": self.noise_margin_db,
            "min_silence_ms": self.min_silence_ms,
            "min_segment_ms": self.min_segment_ms,
            "pad_ms": self.pad_ms,
        }


@dataclass(frozen=True)
class AudioData:
    """A PCM WAV file normalised to float32 samples in [-1, 1)."""

    path: str
    samples: np.ndarray  # shape (frame_count, channels), float32
    sample_rate: int
    channels: int
    sample_width: int  # bytes per sample per channel

    @property
    def total_samples(self) -> int:
        """Length in per-channel sample frames."""
        return int(self.samples.shape[0])


@dataclass(frozen=True)
class Segment:
    """A detected sound region, half-open interval [start_sample, end_sample)."""

    start_sample: int
    end_sample: int

    def length(self) -> int:
        return self.end_sample - self.start_sample


@dataclass(frozen=True)
class Silence:
    """A region classified as silence, half-open [start_sample, end_sample)."""

    start_sample: int
    end_sample: int

    def length(self) -> int:
        return self.end_sample - self.start_sample


@dataclass
class DetectionResult:
    """Output of analysing one file with one parameter set."""

    segments: list[Segment]
    silences: list[Silence]
    sample_rate: int
    total_samples: int
    noise_floor_db: float

    def silence_total_ms(self) -> float:
        total = sum(s.length() for s in self.silences)
        return 1000.0 * total / self.sample_rate if self.sample_rate else 0.0

    def segment_count(self) -> int:
        return len(self.segments)
