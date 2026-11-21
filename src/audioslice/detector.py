"""Silence detection and segmentation over windowed RMS/peak energy.

This module is deliberately free of CLI and database imports; both
:mod:`audioslice.batch` and the command line drive it directly, and tests can
feed it plain NumPy arrays.

Pipeline:

1. :class:`WindowAnalyzer` turns streamed PCM blocks into fixed-size windows,
   carrying partial-window state across blocks so block size never changes the
   numerical result.
2. A calibration pass supplies adaptive on/off RMS thresholds.
3. :func:`label_windows` applies dual-threshold hysteresis (no chattering on
   low-level noise).
4. :func:`_build_segments` enforces minimum segment duration, absorbs short
   pops into neighbouring content without crossing a silence longer than the
   configured cap, and emits half-open ``[start, end)`` sample ranges plus
   their complementary silence regions.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Callable, Iterable, Protocol

import numpy as np

from .calibration import Calibration, Calibrator
from .errors import ConfigError


@dataclass(frozen=True)
class Segment:
    """Half-open sample range ``[start_sample, end_sample)``."""

    start_sample: int
    end_sample: int

    @property
    def length(self) -> int:
        return self.end_sample - self.start_sample

    def as_dict(self) -> dict[str, int]:
        return {"start_sample": self.start_sample, "end_sample": self.end_sample}


@dataclass(frozen=True)
class WindowMetric:
    start_sample: int
    length: int
    rms: float
    peak: float


@dataclass(frozen=True)
class DetectorConfig:
    sample_rate: int
    window_ms: float = 20.0
    noise_margin_db: float = 8.0
    hysteresis_db: float = 3.0
    min_silence_ms: float = 120.0
    min_segment_ms: float = 40.0
    # Pops may only be absorbed across silence shorter than this cap; when
    # ``None`` it defaults to ``min_silence_ms``.
    max_pop_gap_ms: float | None = None
    block_frames: int = 65536

    def __post_init__(self) -> None:
        if self.sample_rate <= 0:
            raise ConfigError("sample rate must be positive")
        if self.window_ms <= 0 or self.min_silence_ms <= 0 or self.min_segment_ms <= 0:
            raise ConfigError("window and duration parameters must be positive")
        if self.hysteresis_db < 0:
            raise ConfigError("hysteresis-db must not be negative")
        if self.block_frames <= 0:
            raise ConfigError("block size must be positive")
        gap = self.min_silence_ms if self.max_pop_gap_ms is None else self.max_pop_gap_ms
        if gap <= 0:
            raise ConfigError("max pop gap must be positive")
        object.__setattr__(self, "max_pop_gap_ms", gap)

    @property
    def window_frames(self) -> int:
        return max(1, int(round(self.sample_rate * self.window_ms / 1000.0)))

    @property
    def min_silence_frames(self) -> int:
        return int(round(self.sample_rate * self.min_silence_ms / 1000.0))

    @property
    def min_segment_frames(self) -> int:
        return int(round(self.sample_rate * self.min_segment_ms / 1000.0))

    @property
    def max_pop_gap_frames(self) -> int:
        return int(round(self.sample_rate * self.max_pop_gap_ms / 1000.0))  # type: ignore[arg-type]

    def param_signature(self) -> dict[str, float | int | None]:
        return {
            "sample_rate": self.sample_rate,
            "window_ms": self.window_ms,
            "noise_margin_db": self.noise_margin_db,
            "hysteresis_db": self.hysteresis_db,
            "min_silence_ms": self.min_silence_ms,
            "min_segment_ms": self.min_segment_ms,
            "max_pop_gap_ms": self.max_pop_gap_ms,
        }


@dataclass(frozen=True)
class DetectionResult:
    sample_rate: int
    total_frames: int
    segments: tuple[Segment, ...]
    silences: tuple[Segment, ...]
    calibration: Calibration
    window_count: int

    @property
    def silence_total_seconds(self) -> float:
        return sum(s.length for s in self.silences) / self.sample_rate if self.sample_rate else 0.0

    def as_dict(self) -> dict[str, object]:
        return {
            "sample_rate": self.sample_rate,
            "total_frames": self.total_frames,
            "segment_count": len(self.segments),
            "segments": [s.as_dict() for s in self.segments],
            "silences": [s.as_dict() for s in self.silences],
            "silence_total_seconds": round(self.silence_total_seconds, 6),
            "calibration": self.calibration.as_dict(),
        }


class WindowAnalyzer:
    """Streaming fixed-window RMS/peak analyser with cross-block carry state."""

    def __init__(self, window_frames: int) -> None:
        if window_frames <= 0:
            raise ConfigError("window size must be positive")
        self.window_frames = window_frames
        self._carry = np.empty(0, dtype=np.float64)
        self._frames_seen = 0

    @staticmethod
    def _to_mono(frames: np.ndarray) -> np.ndarray:
        arr = np.asarray(frames, dtype=np.float64)
        if arr.ndim == 2:
            return arr.mean(axis=1)
        return arr.reshape(-1)

    def process(self, frames: np.ndarray) -> list[WindowMetric]:
        mono = self._to_mono(frames)
        buf = np.concatenate((self._carry, mono)) if self._carry.size else mono
        metrics: list[WindowMetric] = []
        usable = (buf.size // self.window_frames) * self.window_frames
        if usable:
            windows = buf[:usable].reshape(-1, self.window_frames)
            starts = self._frames_seen + np.arange(windows.shape[0]) * self.window_frames
            rms = np.sqrt(np.mean(windows ** 2, axis=1))
            peak = np.max(np.abs(windows), axis=1)
            for start, value, pk in zip(starts, rms, peak):
                metrics.append(WindowMetric(int(start), self.window_frames, float(value), float(pk)))
            self._frames_seen += usable
            self._carry = buf[usable:]
        else:
            self._carry = buf
        return metrics

    def flush(self) -> list[WindowMetric]:
        """Emit the final partial window (shorter than ``window_frames``), if any."""
        if not self._carry.size:
            return []
        metric = WindowMetric(
            self._frames_seen,
            self._carry.size,
            float(np.sqrt(np.mean(self._carry ** 2))),
            float(np.max(np.abs(self._carry))),
        )
        self._frames_seen += self._carry.size
        self._carry = np.empty(0, dtype=np.float64)
        return [metric]


def label_windows(rms: np.ndarray, on_threshold: float, off_threshold: float) -> np.ndarray:
    """Boolean voiced/silent labels with dual-threshold hysteresis.

    A silent window becomes voiced only at/above ``on_threshold``; a voiced
    window stays voiced until energy falls at/below ``off_threshold``. Values
    strictly between the thresholds hold the previous state, which stops
    low-level noise flicker from toggling labels.

    When the recording has no usable dynamic range (the quietest and loudest
    windows differ by less than the configured margin), no threshold can
    separate content from silence.  Digital silence (energy at the numerical
    floor) is then reported as one silent region; any other homogeneous
    recording is reported as a single content segment, so both edge cases have
    stable, defined results.
    """
    if rms.size:
        floor = float(np.maximum(rms.min(), 1e-12))
        spread_db = 20.0 * np.log10(max(rms.max(), floor) / floor)
        if np.isfinite(spread_db):
            margin = 20.0 * np.log10(on_threshold / max(float(rms.min()), 1e-12))
            if spread_db < margin:
                if float(rms.min()) <= 1e-9:
                    return np.zeros(rms.shape, dtype=bool)
                return np.ones(rms.shape, dtype=bool)
    labels = np.zeros(rms.shape, dtype=bool)
    voiced = False
    for i, value in enumerate(rms):
        if not voiced and value >= on_threshold:
            voiced = True
        elif voiced and value <= off_threshold:
            voiced = False
        labels[i] = voiced
    return labels


def _runs(labels: np.ndarray) -> list[tuple[int, int, bool]]:
    if labels.size == 0:
        return []
    change = np.flatnonzero(np.diff(labels.astype(np.int8))) + 1
    bounds = np.concatenate(([0], change, [labels.size]))
    return [(int(a), int(b), bool(labels[a])) for a, b in zip(bounds[:-1], bounds[1:])]


def _build_segments(metrics: list[WindowMetric], labels: np.ndarray,
                    cfg: DetectorConfig) -> tuple[tuple[Segment, ...], tuple[Segment, ...]]:
    total_frames = sum(m.length for m in metrics)
    if total_frames == 0:
        return (), ()
    starts = np.array([m.start_sample for m in metrics])
    lengths = np.array([m.length for m in metrics])
    run_list = _runs(labels)
    voiced_runs = [(s, e) for s, e, voiced in run_list if voiced]
    if not voiced_runs:
        return (), (Segment(0, total_frames),)

    def span(s: int, e: int) -> int:
        return int(starts[e - 1] + lengths[e - 1] - starts[s])

    pops = {(s, e) for s, e in voiced_runs if span(s, e) < cfg.min_segment_frames}
    # Each content-length run is an anchor; absorb neighbouring pops across
    # short silences only. Normal runs are never merged with each other, so
    # content separated by a silence >= the cap always stays split.
    intervals: list[list[int]] = []
    for s, e in voiced_runs:
        if (s, e) in pops:
            continue
        lo, hi = s, e
        # Walk left through runs of pops while each crossed silence is short.
        idx = voiced_runs.index((s, e)) - 1
        while idx >= 0 and (voiced_runs[idx][0], voiced_runs[idx][1]) in pops:
            ps, pe = voiced_runs[idx]
            gap = int(starts[lo] - (starts[pe - 1] + lengths[pe - 1]))
            if gap >= cfg.min_silence_frames or gap > cfg.max_pop_gap_frames:
                break
            lo = ps
            idx -= 1
        # Walk right through pops the same way.
        idx = voiced_runs.index((s, e)) + 1
        while idx < len(voiced_runs) and (voiced_runs[idx][0], voiced_runs[idx][1]) in pops:
            ps, pe = voiced_runs[idx]
            gap = int(starts[ps] - (starts[hi - 1] + lengths[hi - 1]))
            if gap >= cfg.min_silence_frames or gap > cfg.max_pop_gap_frames:
                break
            hi = pe
            idx += 1
        intervals.append([lo, hi])

    # Union intervals that overlap after pop chains join two anchors.
    intervals.sort()
    merged: list[list[int]] = []
    for s, e in intervals:
        if merged and s <= merged[-1][1]:
            merged[-1][1] = max(merged[-1][1], e)
        else:
            merged.append([s, e])

    segments = tuple(
        Segment(int(starts[s]), int(starts[e - 1] + lengths[e - 1]))
        for s, e in merged
        if span(s, e) >= cfg.min_segment_frames
    )
    if not segments:
        return (), (Segment(0, total_frames),)

    silences: list[Segment] = []
    cursor = 0
    for seg in segments:
        if seg.start_sample > cursor:
            silences.append(Segment(cursor, seg.start_sample))
        cursor = seg.end_sample
    if cursor < total_frames:
        silences.append(Segment(cursor, total_frames))
    return segments, tuple(silences)


class BlockFactory(Protocol):
    def __call__(self) -> Iterable[np.ndarray]: ...


def _scan(factory: BlockFactory, cfg: DetectorConfig) -> tuple[list[WindowMetric], Calibration]:
    analyzer = WindowAnalyzer(cfg.window_frames)
    calibrator = Calibrator(cfg.noise_margin_db, cfg.hysteresis_db)
    metrics: list[WindowMetric] = []
    for block in factory():
        for metric in analyzer.process(block):
            metrics.append(metric)
            calibrator.add(np.array([metric.rms]))
    metrics.extend(analyzer.flush())
    calibration = calibrator.finalize()
    return metrics, calibration


def detect_from_blocks(factory: BlockFactory, cfg: DetectorConfig) -> DetectionResult:
    """Two bounded-memory passes: calibrate, then label and segment.

    ``factory`` must return a fresh iterable of PCM blocks on every call
    (the second pass rewinds the source).  Only window metrics are retained,
    never the full audio, so memory stays flat as recordings get longer.
    """
    metrics, calibration = _scan(factory, cfg)
    rms = np.array([m.rms for m in metrics])
    labels = label_windows(rms, calibration.on_threshold, calibration.off_threshold)
    segments, silences = _build_segments(metrics, labels, cfg)
    total_frames = sum(m.length for m in metrics)
    return DetectionResult(cfg.sample_rate, total_frames, segments, silences,
                           calibration, len(metrics))


def detect_samples(samples: np.ndarray, cfg: DetectorConfig) -> DetectionResult:
    """Convenience driver for in-memory arrays (tests, small fixtures)."""
    arr = np.asarray(samples, dtype=np.float64)
    block = cfg.block_frames

    def factory() -> Iterable[np.ndarray]:
        for start in range(0, arr.shape[0], block):
            yield arr[start:start + block]

    return detect_from_blocks(factory, cfg)
