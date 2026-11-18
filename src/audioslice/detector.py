"""Silence detection and segment slicing.

The detector runs a two-threshold hysteresis state machine over a per-frame
RMS envelope. Speech entry requires ``confirm`` consecutive frames above the
upper (open) threshold; a segment closes only after ``min_silence`` worth of
frames stay below the lower (close) threshold, so fades and brief low-level
dips do not fragment an utterance. An adaptive threshold is derived from a
robust low percentile of the recording's own envelope (its noise floor); a
single burst therefore cannot raise the threshold for the whole recording.
"""
from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass, field

import numpy as np

from .signal import DB_FLOOR, frame_rms, ms_to_samples, rms_to_db, smooth_envelope


@dataclass(frozen=True)
class DetectParams:
    frame_ms: float = 20.0
    hop_ms: float = 10.0
    threshold_db: float = -38.0          # absolute threshold (fixed mode only)
    adaptive: bool = True
    noise_percentile: float = 10.0       # robust low-tail noise-floor estimate
    noise_margin_db: float = 8.0         # minimum margin above the noise floor
    hysteresis_db: float = 3.0           # open threshold above close threshold
    confirm_ms: float = 60.0             # consecutive voiced frames to open
    min_silence_ms: float = 180.0        # consecutive quiet frames to close
    merge_gap_ms: float = 90.0           # gaps shorter than this are merged
    smooth_window: int = 1

    def validate(self) -> None:
        if self.frame_ms <= 0 or self.hop_ms <= 0:
            raise ValueError("frame_ms and hop_ms must be positive")
        if self.hop_ms > self.frame_ms:
            raise ValueError("hop_ms must not exceed frame_ms")
        if not (0.0 <= self.noise_percentile <= 100.0):
            raise ValueError("noise_percentile must be within [0, 100]")
        if self.hysteresis_db < 0 or self.noise_margin_db < 0:
            raise ValueError("hysteresis_db and noise_margin_db must be non-negative")
        for name in ("confirm_ms", "min_silence_ms", "merge_gap_ms"):
            if getattr(self, name) < 0:
                raise ValueError(f"{name} must be non-negative")
        if self.smooth_window < 1:
            raise ValueError("smooth_window must be >= 1")

    def to_dict(self) -> dict:
        return dict(sorted(self.__dict__.items()))

    def fingerprint(self) -> str:
        return hashlib.sha256(json.dumps(self.to_dict()).encode()).hexdigest()[:16]


@dataclass
class Region:
    start_sample: int
    end_sample: int

    def to_dict(self) -> dict:
        return {"start_sample": int(self.start_sample), "end_sample": int(self.end_sample)}


@dataclass
class DetectionResult:
    sample_rate: int
    n_samples: int
    close_threshold_db: float
    open_threshold_db: float
    noise_floor_db: float
    segments: list[Region] = field(default_factory=list)       # merged, reported
    raw_segments: list[Region] = field(default_factory=list)   # pre-merge boundaries
    silence_regions: list[Region] = field(default_factory=list)
    silence_total_samples: int = 0

    def to_dict(self) -> dict:
        return {
            "sample_rate": self.sample_rate,
            "n_samples": self.n_samples,
            "close_threshold_db": round(self.close_threshold_db, 4),
            "open_threshold_db": round(self.open_threshold_db, 4),
            "noise_floor_db": round(self.noise_floor_db, 4),
            "segments": [r.to_dict() for r in self.segments],
            "raw_segments": [r.to_dict() for r in self.raw_segments],
            "silence_regions": [r.to_dict() for r in self.silence_regions],
            "silence_total_samples": int(self.silence_total_samples),
            "silence_total_seconds": self.silence_total_samples / self.sample_rate,
        }


class SilenceDetector:
    """Stateless per call: one configured instance is reused across files."""

    def __init__(self, params: DetectParams):
        params.validate()
        self.params = params

    def _thresholds(self, db: np.ndarray) -> tuple[float, float, float]:
        p = self.params
        floor = float(np.percentile(db, p.noise_percentile)) if db.size else DB_FLOOR
        if p.adaptive:
            # The margin is the guaranteed lower bound above the recording's
            # own noise floor; no single burst moves the percentile estimate.
            close = floor + p.noise_margin_db
        else:
            close = p.threshold_db
        return close, close + p.hysteresis_db, floor

    def _raw_segments(
        self,
        db: np.ndarray,
        starts: np.ndarray,
        n_samples: int,
        frame_len: int,
        sample_rate: int,
        open_thr: float,
        close_thr: float,
    ) -> list[Region]:
        p = self.params
        confirm = max(1, int(np.ceil(p.confirm_ms / p.hop_ms)))
        quiet_need = max(1, int(np.ceil(p.min_silence_ms / p.hop_ms)))
        voiced = db >= open_thr
        quiet = db < close_thr
        regions: list[Region] = []
        start: int | None = None
        run_voice = run_quiet = 0
        n = db.size
        for i in range(n):
            if start is None:
                run_voice = run_voice + 1 if voiced[i] else 0
                if run_voice >= confirm:
                    start = int(starts[i - confirm + 1])
                    run_quiet = 0
            else:
                run_quiet = run_quiet + 1 if quiet[i] else 0
                if run_quiet >= quiet_need:
                    last = i - quiet_need  # last frame still considered voiced
                    end = min(n_samples, int(starts[last]) + frame_len)
                    regions.append(Region(start, max(end, start + 1)))
                    start = None
                    run_voice = run_quiet = 0
        if start is not None:  # voice runs to the end of the recording
            regions.append(Region(start, n_samples))
        return regions

    def _silence_from_raw(
        self, raw: list[Region], n_samples: int
    ) -> tuple[list[Region], int]:
        silence: list[Region] = []
        cursor = 0
        for seg in raw:
            if seg.start_sample > cursor:
                silence.append(Region(cursor, seg.start_sample))
            cursor = seg.end_sample
        if cursor < n_samples:
            silence.append(Region(cursor, n_samples))
        # Exact sample accounting: silence is the complement of raw speech.
        total = n_samples - sum(s.end_sample - s.start_sample for s in raw)
        return silence, total

    def _merge(self, raw: list[Region], gap_samples: int) -> list[Region]:
        if not raw:
            return []
        merged = [Region(raw[0].start_sample, raw[0].end_sample)]
        for seg in raw[1:]:
            gap = seg.start_sample - merged[-1].end_sample
            if gap < gap_samples:
                merged[-1].end_sample = seg.end_sample
            else:
                merged.append(Region(seg.start_sample, seg.end_sample))
        return merged

    def detect(self, samples: np.ndarray, sample_rate: int) -> DetectionResult:
        p = self.params
        frame_len = ms_to_samples(p.frame_ms, sample_rate)
        hop = ms_to_samples(p.hop_ms, sample_rate)
        rms, starts = frame_rms(np.asarray(samples, dtype=np.float64), frame_len, hop)
        db = smooth_envelope(rms_to_db(rms), p.smooth_window)
        close_thr, open_thr, floor = self._thresholds(db)
        n_samples = int(np.asarray(samples).shape[0])
        raw = self._raw_segments(
            db, starts, n_samples, frame_len, sample_rate, open_thr, close_thr
        )
        silence, silence_total = self._silence_from_raw(raw, n_samples)
        merged = self._merge(raw, ms_to_samples(p.merge_gap_ms, sample_rate))
        return DetectionResult(
            sample_rate=sample_rate,
            n_samples=n_samples,
            close_threshold_db=close_thr,
            open_threshold_db=open_thr,
            noise_floor_db=floor,
            segments=merged,
            raw_segments=raw,
            silence_regions=silence,
            silence_total_samples=silence_total,
        )
