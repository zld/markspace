"""Self-contained silence / speech detection.

The detector never loads a whole recording: PCM is consumed in fixed time
blocks (2 s by default) via :class:`audiocut.wavio.WavReader`, split into
short analysis frames, and a frame-level RMS series drives an adaptive
dual-threshold (hysteresis) state machine.  Frame boundaries are then refined
to individual samples by short random-access reads around each boundary.
"""

from __future__ import annotations

import hashlib
import json
from dataclasses import asdict, dataclass, field

import numpy as np

from . import thresholds
from .wavio import WavReader


@dataclass(frozen=True)
class DetectorConfig:
    """Tunable detection parameters (all durations in seconds unless noted)."""

    frame_ms: float = 20.0
    block_seconds: float = 2.0
    noise_quantile: float = 0.15
    voice_factor: float = 4.0
    silence_factor: float = 2.5
    min_segment_s: float = 0.12
    min_silence_s: float = 0.10
    merge_gap_s: float = 0.20

    def validate(self) -> None:
        if self.frame_ms <= 0:
            raise ValueError("frame_ms must be greater than zero")
        if self.block_seconds <= 0:
            raise ValueError("block_seconds must be greater than zero")
        if not 0.0 < self.noise_quantile < 1.0:
            raise ValueError("noise_quantile must lie strictly between 0 and 1")
        if self.silence_factor <= 1.0 or self.voice_factor <= 1.0:
            raise ValueError("threshold factors must be greater than 1")
        if self.voice_factor <= self.silence_factor:
            raise ValueError(
                "voice_factor must be greater than silence_factor "
                "(high threshold must stay above low threshold)"
            )
        for name in ("min_segment_s", "min_silence_s", "merge_gap_s"):
            if getattr(self, name) < 0:
                raise ValueError(f"{name} must not be negative")
        if self.merge_gap_s < self.min_silence_s:
            raise ValueError(
                "merge_gap_s must be >= min_silence_s, otherwise every merged "
                "gap would be shorter than the minimum silence duration"
            )

    def fingerprint(self) -> str:
        payload = json.dumps(asdict(self), sort_keys=True)
        return hashlib.sha256(payload.encode("utf-8")).hexdigest()


@dataclass(frozen=True)
class Segment:
    index: int
    start_sample: int
    end_sample: int

    @property
    def length(self) -> int:
        return self.end_sample - self.start_sample


@dataclass(frozen=True)
class SilenceRegion:
    start_sample: int
    end_sample: int

    @property
    def length(self) -> int:
        return self.end_sample - self.start_sample


@dataclass
class DetectionResult:
    path: str
    sample_rate: int
    n_frames_total: int
    frame_len: int
    config_fingerprint: str
    noise_floor: float
    low_threshold: float
    high_threshold: float
    segments: list[Segment] = field(default_factory=list)
    silences: list[SilenceRegion] = field(default_factory=list)
    silence_total_seconds: float = 0.0


class SilenceDetector:
    """Adaptive dual-threshold detector reused by analyze and batch export."""

    def __init__(self, config: DetectorConfig | None = None):
        self.config = config or DetectorConfig()
        self.config.validate()

    # ------------------------------------------------------------------ pass 1
    def _frame_rms(self, reader: WavReader) -> tuple[list[int], np.ndarray]:
        """Stream blocks; return per-frame start samples and RMS values."""

        frame_len = max(1, int(round(self.config.frame_ms * reader.sample_rate / 1000.0)))
        carry = np.empty(0, dtype=np.float32)
        carry_start = 0
        starts: list[int] = []
        rms_values: list[float] = []

        for block_start, block in reader.iter_blocks(self.config.block_seconds):
            if carry.size:
                stream = np.concatenate((carry, block))
                stream_start = carry_start
            else:
                stream = block
                stream_start = block_start
            n_full = stream.size // frame_len
            if n_full:
                frames = stream[: n_full * frame_len].reshape(n_full, frame_len)
                rms = np.sqrt(np.mean(frames.astype(np.float64) ** 2, axis=1))
                for i, value in enumerate(rms):
                    starts.append(stream_start + i * frame_len)
                    rms_values.append(float(value))
            carry = stream[n_full * frame_len :]
            carry_start = stream_start + n_full * frame_len

        # Trailing residual frame (may be shorter than frame_len).
        if carry.size:
            starts.append(carry_start)
            rms_values.append(float(np.sqrt(np.mean(carry.astype(np.float64) ** 2))))
        return starts, np.asarray(rms_values, dtype=np.float64)

    # ------------------------------------------------------------- thresholds
    def _thresholds(self, rms: np.ndarray) -> tuple[float, float, float, float]:
        return thresholds.estimate(
            rms,
            self.config.noise_quantile,
            self.config.voice_factor,
            self.config.silence_factor,
        )

    @staticmethod
    def _voiced_runs(rms: np.ndarray, t_lo: float, t_hi: float) -> list[tuple[int, int]]:
        """Hysteresis state machine; runs are (first_frame, last_frame_inclusive)."""

        runs: list[tuple[int, int]] = []
        voiced, start = False, 0
        for i, value in enumerate(rms):
            if not voiced and value > t_hi:
                voiced, start = True, i
            elif voiced and value < t_lo:
                voiced = False
                runs.append((start, i - 1))
        if voiced:
            runs.append((start, len(rms) - 1))
        return runs

    # ---------------------------------------------------------- post-processing
    def _merge_and_filter(
        self, runs: list[tuple[int, int]], frame_starts: list[int], sample_rate: int, n_samples: int
    ) -> list[tuple[int, int]]:
        def frame_end_sample(last_frame: int) -> int:
            return frame_starts[last_frame + 1] if last_frame + 1 < len(frame_starts) else n_samples

        # Merge runs separated by gaps shorter than merge_gap_s.
        merged: list[list[int]] = []
        for first, last in runs:
            if merged:
                gap = frame_starts[first] - frame_end_sample(merged[-1][1])
                if gap / sample_rate < self.config.merge_gap_s:
                    merged[-1][1] = last
                    continue
            merged.append([first, last])

        # Drop segments shorter than min_segment_s.
        kept = [(f, l) for f, l in merged
                if (frame_end_sample(l) - frame_starts[f]) / sample_rate
                >= self.config.min_segment_s]
        return kept

    # --------------------------------------------------------- sample refinement
    def _refine(
        self,
        reader: WavReader,
        frame_runs: list[tuple[int, int]],
        frame_starts: list[int],
        t_lo: float,
        n_samples: int,
    ) -> list[Segment]:
        frame_len = max(1, int(round(self.config.frame_ms * reader.sample_rate / 1000.0)))

        def frame_end(last_frame: int) -> int:
            if last_frame + 1 < len(frame_starts):
                return frame_starts[last_frame + 1]
            return n_samples

        rough: list[tuple[int, int]] = []
        for first, last in frame_runs:
            win_start = frame_starts[first - 1] if first > 0 else frame_starts[first]
            win_end = min(frame_end(first) + frame_len, n_samples)
            head = reader.read_range_mono(win_start, win_end)
            above = head > t_lo
            # First sample beginning a run of 3 consecutive above-threshold samples.
            crossings = np.flatnonzero(above[:-2] & above[1:-1] & above[2:])
            refined_start = int(win_start + crossings[0]) if crossings.size else frame_starts[first]

            tail_start = frame_starts[last]
            tail_end = min(frame_end(last) + frame_len, n_samples)
            tail = reader.read_range_mono(tail_start, tail_end)
            above = tail > t_lo
            crossings = np.flatnonzero(above[:-2] & above[1:-1] & above[2:])
            if crossings.size:
                refined_end = int(tail_start + crossings[-1] + 3)
            else:
                refined_end = frame_end(last)
            rough.append((refined_start, refined_end))

        # Guarantee ordering, non-emptiness, and bounds inside the file.
        segments: list[Segment] = []
        prev_end = 0
        for i, (start, end) in enumerate(rough):
            start = max(0, start, prev_end)
            end = min(n_samples, max(end, start + 1))
            segments.append(Segment(index=i, start_sample=start, end_sample=end))
            prev_end = end
        return segments

    @staticmethod
    def _silence_complement(
        segments: list[Segment], n_samples: int, sample_rate: int, min_silence_s: float
    ) -> tuple[list[SilenceRegion], float]:
        regions: list[SilenceRegion] = []
        cursor = 0
        for seg in segments:
            if seg.start_sample > cursor:
                regions.append(SilenceRegion(cursor, seg.start_sample))
            cursor = seg.end_sample
        if cursor < n_samples:
            regions.append(SilenceRegion(cursor, n_samples))
        min_len = min_silence_s * sample_rate
        regions = [r for r in regions if r.length >= min_len]
        total = sum(r.length for r in regions) / sample_rate
        return regions, total

    # -------------------------------------------------------------------- main
    def analyze(self, reader: WavReader) -> DetectionResult:
        n_samples = reader.n_frames
        frame_len = max(1, int(round(self.config.frame_ms * reader.sample_rate / 1000.0)))
        if frame_len <= 0:
            raise ValueError("frame length in samples must be greater than zero")

        frame_starts, rms = self._frame_rms(reader)
        if rms.size == 0:  # Empty input.
            return DetectionResult(
                path=reader.path,
                sample_rate=reader.sample_rate,
                n_frames_total=0,
                frame_len=frame_len,
                config_fingerprint=self.config.fingerprint(),
                noise_floor=0.0,
                low_threshold=0.0,
                high_threshold=0.0,
            )

        noise, t_lo, t_hi, _ = self._thresholds(rms)
        runs = self._voiced_runs(rms, t_lo, t_hi)
        kept = self._merge_and_filter(runs, frame_starts, reader.sample_rate, n_samples)
        segments = self._refine(reader, kept, frame_starts, t_lo, n_samples)
        silences, total_s = self._silence_complement(
            segments, n_samples, reader.sample_rate, self.config.min_silence_s
        )
        return DetectionResult(
            path=reader.path,
            sample_rate=reader.sample_rate,
            n_frames_total=len(frame_starts),
            frame_len=frame_len,
            config_fingerprint=self.config.fingerprint(),
            noise_floor=noise,
            low_threshold=t_lo,
            high_threshold=t_hi,
            segments=segments,
            silences=silences,
            silence_total_seconds=total_s,
        )

    def analyze_path(self, path: str) -> DetectionResult:
        with WavReader(path) as reader:
            return self.analyze(reader)
