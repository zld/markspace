"""Adaptive activity/silence detector.

Pipeline per file:

1. Hysteresis classification against a *local* sliding-quantile noise floor.
   Frames cross into activity above ``high_factor * floor`` and only fall back
   below ``low_factor * floor``; the gap between the two thresholds prevents
   chatter on slowly fluctuating hum.
2. Boundary trimming via the continuous log-energy slope, so gradual
   fade-ins/fade-out are claimed as part of the event instead of split off.
3. Minimum-duration filtering and merging of neighbours whose gap is within
   ``merge_gap_ms``; original candidate bounds and the reason for each merge
   are preserved on the segment records.
4. Silence is the exact complement of the final activity regions.

All coordinates are sample positions in half-open intervals ``[start, end)``.
One detector instance holds one configuration and is reused for every file in
a batch.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Sequence

import numpy as np

from .features import ENERGY_FLOOR, FrameFeatures

# Absolute RMS floor so a digital-silence recording cannot produce zero
# thresholds (which would classify everything as active).
ABSOLUTE_FLOOR = 1.0


@dataclass(frozen=True)
class Segment:
    """A half-open sample interval [start_sample, end_sample)."""

    start_sample: int
    end_sample: int
    kind: str  # "activity" or "silence"
    reason: str = ""
    # For merged activity segments: candidate intervals kept pre-merge.
    candidates: tuple[tuple[int, int], ...] = field(default_factory=tuple)

    @property
    def length_samples(self) -> int:
        return self.end_sample - self.start_sample


@dataclass(frozen=True)
class DetectorConfig:
    frame_ms: float = 10.0
    noise_window_ms: float = 500.0
    noise_quantile: float = 0.25
    high_factor: float = 3.0
    low_factor: float = 1.8
    slope_factor: float = 2.0
    # Absolute RMS activation path: a frame this far above digital silence is
    # active regardless of the local relative floor. Required so a constant,
    # uniformly loud signal (whose sliding quantile equals itself) is still
    # detected rather than mistaken for noise.
    abs_activation_rms: float = 40.0
    min_event_ms: float = 40.0
    merge_gap_ms: float = 70.0
    max_slope_frames: int = 12

    def validate(self) -> None:
        """Fail fast on illegal parameters, before any file is processed."""
        positive = ("frame_ms", "noise_window_ms", "min_event_ms")
        for name in positive:
            if getattr(self, name) <= 0:
                raise ValueError(f"{name} must be > 0, got {getattr(self, name)!r}")
        if not 0.0 < self.noise_quantile < 1.0:
            raise ValueError("noise_quantile must be within (0, 1)")
        if self.low_factor <= 1.0 or self.high_factor < self.low_factor:
            raise ValueError("require high_factor >= low_factor > 1")
        if self.slope_factor <= 0:
            raise ValueError("slope_factor must be > 0")
        if self.abs_activation_rms <= 0:
            raise ValueError("abs_activation_rms must be > 0")
        if self.merge_gap_ms < 0:
            raise ValueError("merge_gap_ms must be >= 0")
        if self.max_slope_frames <= 0:
            raise ValueError("max_slope_frames must be > 0")
        if self.min_event_ms < self.frame_ms:
            raise ValueError("min_event_ms must be >= frame_ms")

    def signature(self) -> str:
        """Stable textual form used as part of the storage dedup key."""
        return (
            f"frame_ms={self.frame_ms};noise_window_ms={self.noise_window_ms};"
            f"noise_quantile={self.noise_quantile};high_factor={self.high_factor};"
            f"low_factor={self.low_factor};slope_factor={self.slope_factor};"
            f"abs_activation_rms={self.abs_activation_rms};"
            f"min_event_ms={self.min_event_ms};merge_gap_ms={self.merge_gap_ms};"
            f"max_slope_frames={self.max_slope_frames}"
        )


@dataclass(frozen=True)
class DetectionResult:
    activity: tuple[Segment, ...]
    silence: tuple[Segment, ...]
    n_frames: int
    total_samples: int


class ActivityDetector:
    """Reusable, configuration-bound activity/silence detector."""

    def __init__(self, config: DetectorConfig | None = None):
        self.config = config or DetectorConfig()
        self.config.validate()

    # -- public API -------------------------------------------------------

    def detect(self, feats: FrameFeatures) -> DetectionResult:
        total = feats.frame_start_sample(feats.n_frames - 1) + feats.frame_length
        if feats.n_frames == 0:
            return DetectionResult((), (), 0, total)

        floor = np.maximum(feats.noise_floor, ABSOLUTE_FLOOR)
        high = self.config.high_factor * floor
        low = self.config.low_factor * floor
        abs_on = feats.energy >= self.config.abs_activation_rms
        active = self._hysteresis(feats.energy, high, low, abs_on)
        raw = self._runs(active)

        candidates: list[tuple[int, int]] = []
        for a, b in raw:
            a, b = self._trim_by_slope(a, b, feats, floor)
            candidates.append((a, b))

        kept = self._filter_min_duration(candidates, feats)
        merged, reasons = self._merge(kept, feats)
        activity = self._to_segments(merged, candidates, reasons, kept, feats)
        silence = self._silence_complement(activity, total)
        return DetectionResult(
            activity=tuple(activity),
            silence=tuple(silence),
            n_frames=feats.n_frames,
            total_samples=total,
        )

    # -- steps ------------------------------------------------------------

    @staticmethod
    def _hysteresis(energy: np.ndarray, high: np.ndarray, low: np.ndarray,
                    abs_on: np.ndarray) -> np.ndarray:
        """Two-entry hysteresis: relative threshold OR absolute activation.

        Entry happens when energy clears the local high threshold or the
        absolute activation level; exit only when it drops below the local
        low threshold *and* the absolute level. This keeps a uniformly loud
        signal active without letting steady low-level hum self-trigger.
        """
        out = np.zeros(energy.shape[0], dtype=bool)
        state = False
        for i, e in enumerate(energy):
            enter = e >= high[i] or abs_on[i]
            leave = e < low[i] and not abs_on[i]
            if not state and enter:
                state = True
            elif state and leave:
                state = False
            out[i] = state
        return out

    @staticmethod
    def _runs(mask: np.ndarray) -> list[tuple[int, int]]:
        if not mask.any():
            return []
        edges = np.diff(mask.astype(np.int8))
        starts = list(np.where(edges == 1)[0] + 1)
        ends = list(np.where(edges == -1)[0] + 1)
        if mask[0]:
            starts = [0] + starts
        if mask[-1]:
            ends = ends + [mask.shape[0]]
        return list(zip(starts, ends))

    def _trim_by_slope(self, a: int, b: int, feats: FrameFeatures,
                       floor: np.ndarray) -> tuple[int, int]:
        """Grow a candidate over steady fade-in/fade-out frames."""
        cfg = self.config
        limit = cfg.max_slope_frames
        # Fade-in: walk left while energy keeps rising meaningfully.
        i = a
        while i > 0 and a - i < limit and feats.slope[i] > 0:
            if feats.energy[i - 1] >= cfg.slope_factor * ENERGY_FLOOR and \
                    feats.energy[i - 1] >= floor[i - 1] / cfg.low_factor:
                i -= 1
            else:
                break
        start = i
        # Fade-out: walk right while energy keeps falling meaningfully.
        j = b
        n = feats.n_frames
        while j < n and j - b < limit and feats.slope[j] < 0:
            if feats.energy[j] >= cfg.slope_factor * ENERGY_FLOOR and \
                    feats.energy[j] >= floor[j] / cfg.low_factor:
                j += 1
            else:
                break
        return start, j

    def _filter_min_duration(self, candidates: Sequence[tuple[int, int]],
                             feats: FrameFeatures) -> list[tuple[int, int]]:
        min_frames = max(1, int(round(self.config.min_event_ms / self.config.frame_ms)))
        return [(a, b) for a, b in candidates if b - a >= min_frames]

    def _merge(self, kept: Sequence[tuple[int, int]],
               feats: FrameFeatures) -> tuple[list[tuple[int, int]], list[str]]:
        if not kept:
            return [], []
        gap_frames = max(0, int(round(self.config.merge_gap_ms / self.config.frame_ms)))
        merged: list[list[int]] = [[kept[0][0], kept[0][1]]]
        reasons: list[str] = ["initial"]
        for a, b in kept[1:]:
            if a - merged[-1][1] <= gap_frames:
                merged[-1][1] = b
                reasons[-1] = "merged_gap_lt_merge_gap_ms"
            else:
                merged.append([a, b])
                reasons.append("initial")
        return [(a, b) for a, b in merged], reasons

    def _to_segments(self, merged: Sequence[tuple[int, int]],
                     candidates: Sequence[tuple[int, int]], reasons: Sequence[str],
                     kept: Sequence[tuple[int, int]],
                     feats: FrameFeatures) -> list[Segment]:
        segs: list[Segment] = []
        for (a, b), reason in zip(merged, reasons):
            start = feats.frame_start_sample(a)
            end = min(feats.frame_start_sample(b - 1) + feats.frame_length,
                      feats.frame_start_sample(feats.n_frames - 1) + feats.frame_length)
            member = tuple(
                (feats.frame_start_sample(x),
                 feats.frame_start_sample(y - 1) + feats.frame_length)
                for x, y in candidates if x >= a and y <= b and (x, y) in kept
            )
            segs.append(Segment(
                start_sample=start, end_sample=end, kind="activity",
                reason=reason,
                candidates=member if reason.startswith("merged") else (),
            ))
        return segs

    @staticmethod
    def _silence_complement(activity: Sequence[Segment], total: int) -> list[Segment]:
        silence: list[Segment] = []
        cursor = 0
        for seg in activity:
            if seg.start_sample > cursor:
                silence.append(Segment(cursor, seg.start_sample, "silence", "complement"))
            cursor = seg.end_sample
        if cursor < total:
            silence.append(Segment(cursor, total, "silence", "complement"))
        return silence
