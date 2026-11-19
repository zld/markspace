"""Core domain types: sample ranges, analysis parameters and detection results.

All time intervals are expressed as integer sample counts using half-open
ranges ``[start_sample, end_sample)``.  Durations configured in milliseconds
are converted to sample counts once, at parameter construction time, so every
downstream module works with plain integers.
"""

from __future__ import annotations

from dataclasses import dataclass

#: PCM sample formats this tool understands.
SUPPORTED_SAMPLE_WIDTHS = frozenset({1, 2, 3, 4})
#: Maximum number of interleaved channels handled by the analyzer.
SUPPORTED_CHANNELS = frozenset({1, 2})


@dataclass(frozen=True)
class Region:
    """A half-open sample interval tagged as either ``segment`` or ``silence``."""

    kind: str
    start_sample: int
    end_sample: int

    def __post_init__(self) -> None:
        if self.kind not in ("segment", "silence"):
            raise ValueError(f"region kind must be 'segment' or 'silence', got {self.kind!r}")
        if self.start_sample < 0 or self.end_sample < 0:
            raise ValueError("region boundaries must be non-negative")
        if self.end_sample < self.start_sample:
            raise ValueError("region end must not precede its start")

    @property
    def length_samples(self) -> int:
        return self.end_sample - self.start_sample

    def is_empty(self) -> bool:
        return self.end_sample == self.start_sample


@dataclass(frozen=True)
class AnalysisParams:
    """User-tunable detection parameters, normalized to integer sample counts."""

    sample_rate: int
    frame_samples: int
    min_segment_samples: int
    min_silence_samples: int
    margin_db: float
    hysteresis_db: float
    noise_percentile: float

    @staticmethod
    def from_milliseconds(
        sample_rate: int,
        frame_ms: float,
        min_segment_ms: float,
        min_silence_ms: float,
        margin_db: float = 9.0,
        hysteresis_db: float = 3.0,
        noise_percentile: float = 20.0,
    ) -> "AnalysisParams":
        if sample_rate <= 0:
            raise ValueError("sample_rate must be positive")
        if frame_ms <= 0:
            raise ValueError("frame_ms must be positive")
        if min_segment_ms < 0 or min_silence_ms < 0:
            raise ValueError("minimum duration parameters must be non-negative")
        if margin_db <= 0 or hysteresis_db <= 0:
            raise ValueError("level margins must be positive")
        if not 0.0 < noise_percentile < 100.0:
            raise ValueError("noise_percentile must lie within (0, 100)")

        def ms_to_samples(ms: float) -> int:
            return max(1, int(round(ms * sample_rate / 1000.0)))

        return AnalysisParams(
            sample_rate=sample_rate,
            frame_samples=ms_to_samples(frame_ms),
            min_segment_samples=ms_to_samples(min_segment_ms),
            min_silence_samples=ms_to_samples(min_silence_ms),
            margin_db=float(margin_db),
            hysteresis_db=float(hysteresis_db),
            noise_percentile=float(noise_percentile),
        )


@dataclass(frozen=True)
class AudioData:
    """Validated PCM audio as a float64 mono mix plus source metadata."""

    samples: "object"  # numpy.ndarray, shape (n_samples,), dtype float64, range [-1, 1]
    sample_rate: int
    channels: int
    sample_width_bytes: int


@dataclass(frozen=True)
class DetectionResult:
    """The complete output of one detection run over a single file."""

    regions: tuple[Region, ...]
    noise_floor_rms: float
    enter_threshold_rms: float
    exit_threshold_rms: float
    frame_count: int
    total_samples: int

    @property
    def segments(self) -> tuple[Region, ...]:
        return tuple(r for r in self.regions if r.kind == "segment")

    @property
    def silences(self) -> tuple[Region, ...]:
        return tuple(r for r in self.regions if r.kind == "silence")
