"""Detection configuration and validation.

All user-facing detection parameters live here.  Values are validated before
any input is opened so that an invalid batch is rejected as a whole.
"""

from __future__ import annotations

import hashlib
import json
import math
from dataclasses import asdict, dataclass
from typing import Any


class ConfigError(ValueError):
    """Raised when detection parameters are invalid."""


@dataclass(frozen=True)
class DetectionConfig:
    frame_ms: float = 10.0
    noise_percentile: float = 20.0
    noise_window_ms: float = 1000.0
    margin_db: float = 8.0
    hysteresis_db: float = 4.0
    onset_frames: int = 2
    release_frames: int = 5
    merge_ms: float = 35.0
    min_segment_ms: float = 25.0
    fade_ms: float = 5.0
    min_peak_dbfs: float = -70.0
    max_file_mb: float = 512.0
    min_sample_rate: int = 8000
    max_sample_rate: int = 384_000

    def validate(self) -> None:
        float_fields = {
            "frame_ms": (self.frame_ms, 1.0, 500.0),
            "noise_percentile": (self.noise_percentile, 0.0, 100.0),
            "noise_window_ms": (self.noise_window_ms, 10.0, 60_000.0),
            "margin_db": (self.margin_db, 0.5, 60.0),
            "hysteresis_db": (self.hysteresis_db, 0.0, 40.0),
            "merge_ms": (self.merge_ms, 0.0, 10_000.0),
            "min_segment_ms": (self.min_segment_ms, 0.0, 60_000.0),
            "fade_ms": (self.fade_ms, 0.0, 10_000.0),
            "min_peak_dbfs": (self.min_peak_dbfs, -120.0, 0.0),
            "max_file_mb": (self.max_file_mb, 1.0, 4096.0),
        }
        for name, (value, low, high) in float_fields.items():
            if isinstance(value, bool) or not isinstance(value, (int, float)):
                raise ConfigError(f"{name} must be a number")
            if not math.isfinite(float(value)):
                raise ConfigError(f"{name} must be finite")
            if not low <= float(value) <= high:
                raise ConfigError(f"{name} must be between {low} and {high}")
        if self.noise_percentile >= 100.0:
            raise ConfigError("noise_percentile must be below 100")
        if self.onset_frames < 1 or self.onset_frames > 1000:
            raise ConfigError("onset_frames must be between 1 and 1000")
        if self.release_frames < 0 or self.release_frames > 1000:
            raise ConfigError("release_frames must be between 0 and 1000")
        if self.min_sample_rate < 1000 or self.max_sample_rate > 384_000:
            raise ConfigError("sample rate limits must be within 1000..384000")
        if self.min_sample_rate > self.max_sample_rate:
            raise ConfigError("min_sample_rate must not exceed max_sample_rate")

    @property
    def high_margin_db(self) -> float:
        return self.margin_db

    @property
    def low_margin_db(self) -> float:
        return max(0.5, self.margin_db - self.hysteresis_db)

    def frame_samples(self, sample_rate: int) -> int:
        self.validate_sample_rate(sample_rate)
        return max(2, int(round(sample_rate * self.frame_ms / 1000.0)))

    def merge_samples(self, sample_rate: int) -> int:
        return int(round(sample_rate * self.merge_ms / 1000.0))

    def min_segment_samples(self, sample_rate: int) -> int:
        return int(round(sample_rate * self.min_segment_ms / 1000.0))

    def fade_samples(self, sample_rate: int) -> int:
        return int(round(sample_rate * self.fade_ms / 1000.0))

    def noise_window_frames(self) -> int:
        # Frame length cancels out of the window/frame ratio.
        return max(1, int(round(self.noise_window_ms / self.frame_ms)))

    def validate_sample_rate(self, sample_rate: int) -> None:
        if not isinstance(sample_rate, int) or isinstance(sample_rate, bool):
            raise ConfigError("sample rate must be an integer")
        if not self.min_sample_rate <= sample_rate <= self.max_sample_rate:
            raise ConfigError(
                f"sample rate {sample_rate} outside "
                f"{self.min_sample_rate}..{self.max_sample_rate}"
            )

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)

    def canonical_json(self) -> str:
        return json.dumps(self.to_dict(), sort_keys=True, separators=(",", ":"))

    def digest(self) -> str:
        return hashlib.sha256(self.canonical_json().encode("utf-8")).hexdigest()
