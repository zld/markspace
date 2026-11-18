"""Short-time energy detection with noise-floor tracking and hysteresis.

The detector streams samples in bounded chunks and keeps only one RMS value
per frame, so memory stays flat for long files. The hysteresis decision runs
over the frame-RMS series in ``finish()`` because the noise floor (a quantile
of the whole series) is only known once every frame has been seen.
"""
from __future__ import annotations

from dataclasses import dataclass

import numpy as np

_MIN_FLOOR = 1e-8  # guards against digital silence producing a zero floor


@dataclass(frozen=True)
class DetectorParams:
    frame_ms: float = 25.0
    hop_ms: float = 10.0
    noise_quantile: float = 0.3  # quantile of frame RMS taken as the noise floor
    onset_factor: float = 3.0    # enter activity at floor * onset_factor
    offset_factor: float = 1.5   # leave activity below floor * offset_factor

    def validate(self) -> None:
        if self.frame_ms <= 0:
            raise ValueError("frame_ms must be positive")
        if self.hop_ms <= 0:
            raise ValueError("hop_ms must be positive")
        if self.hop_ms > self.frame_ms:
            raise ValueError("hop_ms must not exceed frame_ms")
        if not 0.0 < self.noise_quantile < 1.0:
            raise ValueError("noise_quantile must be in (0, 1)")
        if self.offset_factor < 1.0:
            raise ValueError("offset_factor must be >= 1.0")
        if self.onset_factor <= self.offset_factor:
            raise ValueError("onset_factor must exceed offset_factor (hysteresis)")


@dataclass(frozen=True)
class RawRegion:
    """Active run in frame units; end is exclusive."""

    start_frame: int
    end_frame: int


@dataclass
class DetectionResult:
    sample_rate: int
    frame: int
    hop: int
    num_samples: int
    frame_rms: np.ndarray
    noise_floor: float
    high_threshold: float
    low_threshold: float
    regions: list[RawRegion]
    params: DetectorParams


class Detector:
    """Streaming frame-RMS computer shared by single-file and batch paths."""

    def __init__(self, params: DetectorParams, sample_rate: int):
        params.validate()
        self.params = params
        self.sample_rate = sample_rate
        self.frame = max(1, round(sample_rate * params.frame_ms / 1000.0))
        self.hop = max(1, round(sample_rate * params.hop_ms / 1000.0))
        self._buf = np.empty(0, dtype=np.float32)
        self._rms: list[float] = []
        self._fed = 0

    def feed(self, samples: np.ndarray) -> None:
        """Consume one chunk of mono samples; state carries across chunks."""
        samples = np.asarray(samples, dtype=np.float32)
        if samples.size:
            self._buf = np.concatenate([self._buf, samples])
            self._fed += int(samples.size)
        while self._buf.size >= self.frame:
            win = self._buf[: self.frame]
            self._rms.append(float(np.sqrt(np.mean(win * win))))
            self._buf = self._buf[self.hop :]

    def finish(self, num_samples: int | None = None) -> DetectionResult:
        if self._buf.size:
            win = self._buf  # final partial frame still carries energy
            self._rms.append(float(np.sqrt(np.mean(win * win))))
            self._buf = np.empty(0, dtype=np.float32)
        total = self._fed if num_samples is None else num_samples
        rms = np.asarray(self._rms, dtype=np.float64)
        noise = float(np.quantile(rms, self.params.noise_quantile)) if rms.size else 0.0
        noise = max(noise, _MIN_FLOOR)
        high = noise * self.params.onset_factor
        low = noise * self.params.offset_factor
        regions: list[RawRegion] = []
        active = False
        start = 0
        for i, value in enumerate(rms):
            if not active:
                if value >= high:
                    active, start = True, i
            elif value < low:
                active = False
                regions.append(RawRegion(start, i))
        if active:
            regions.append(RawRegion(start, len(rms)))
        return DetectionResult(
            sample_rate=self.sample_rate,
            frame=self.frame,
            hop=self.hop,
            num_samples=total,
            frame_rms=rms,
            noise_floor=noise,
            high_threshold=high,
            low_threshold=low,
            regions=regions,
            params=self.params,
        )
