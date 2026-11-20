"""Energy-based silence detection.

Computes per-window RMS over each channel and takes the maximum across
channels so anti-phase stereo material cannot cancel out. The silence
threshold adapts to the recording: a low quantile of the window levels
estimates the noise floor, and the threshold may be lowered to
noise + margin (never raised above the configured value) so soft
onsets in quiet recordings are not swallowed. A 6 dB hysteresis keeps
the state machine from chattering, and low-level gaps shorter than
min_silence_ms are merged into the neighbouring clips instead of
becoming cut points.

Intervals are half-open sample ranges [start, end). Segments and
silence intervals together cover [0, sample_frames) without overlap.
"""
from __future__ import annotations

from typing import Iterable, Iterator, List, Tuple

import numpy as np

from . import (
    HYSTERESIS_DB,
    MIN_DB,
    DetectionResult,
    DetectorParams,
    Segment,
)

# Quantile of window levels used to estimate the noise floor.
NOISE_QUANTILE = 0.1


class RmsFramer:
    """Streaming per-window RMS computer with bounded memory.

    Feed decoded sample blocks of shape (frames, channels); each call
    returns the max-across-channels RMS of every complete window that
    became available. Only a sub-window remainder is kept between calls.
    """

    def __init__(self, frame: int, hop: int, channels: int):
        if frame <= 0 or hop <= 0:
            raise ValueError("frame and hop must be positive")
        self.frame = frame
        self.hop = hop
        self.channels = channels
        self._buf = np.empty((0, channels), dtype=np.float64)

    def feed(self, block: np.ndarray) -> List[float]:
        if block.size:
            self._buf = np.concatenate((self._buf, block))
        out: List[float] = []
        frame, hop = self.frame, self.hop
        n = self._buf.shape[0]
        pos = 0
        while pos + frame <= n:
            window = self._buf[pos : pos + frame]
            rms = np.sqrt(np.mean(window * window, axis=0)).max()
            out.append(float(rms))
            pos += hop
        self._buf = self._buf[pos:]
        return out

    def flush(self) -> List[float]:
        """Emit one final partial window for the tail, if any remains."""
        buf = self._buf
        self._buf = self._buf[:0]
        if buf.shape[0] == 0:
            return []
        rms = np.sqrt(np.mean(buf * buf, axis=0)).max()
        return [float(rms)]


def levels_to_db(rms_values: Iterable[float]) -> np.ndarray:
    """Convert RMS values to dBFS, flooring silence at MIN_DB."""
    rms = np.asarray(list(rms_values), dtype=np.float64)
    if rms.size == 0:
        return rms
    floor = 10.0 ** (MIN_DB / 20.0)
    return 20.0 * np.log10(np.maximum(rms, floor))


def effective_threshold(db_levels: np.ndarray, params: DetectorParams) -> float:
    """Silence threshold after noise-floor adaptation.

    The noise estimate may only lower the configured threshold, never
    raise it: threshold = min(silence_dbfs, noise_floor + margin).
    """
    if db_levels.size == 0:
        return params.silence_dbfs
    noise_db = float(np.quantile(db_levels, NOISE_QUANTILE))
    return min(params.silence_dbfs, noise_db + params.noise_margin_db)


def silence_window_runs(db_levels: np.ndarray, threshold: float) -> List[Tuple[int, int]]:
    """Hysteresis state machine over window levels.

    Returns (first_window, one_past_last_window) runs of the silent
    state. Entering silence requires a level below `threshold`;
    leaving it requires a level above threshold + HYSTERESIS_DB.
    """
    n = int(db_levels.shape[0])
    if n == 0:
        return []
    low = threshold
    high = threshold + HYSTERESIS_DB
    runs: List[Tuple[int, int]] = []
    silent = bool(db_levels[0] < low)
    start = 0
    for i in range(1, n):
        if silent:
            if db_levels[i] > high:
                runs.append((start, i))
                silent = False
        elif db_levels[i] < low:
            start = i
            silent = True
    if silent:
        runs.append((start, n))
    return runs


def _runs_to_samples(
    runs: List[Tuple[int, int]], n_windows: int, hop: int, total_frames: int
) -> List[Segment]:
    # Every window k starts at sample k*hop (flush windows included), so
    # both run edges map onto the hop grid; the final edge is clamped to
    # the file length. This keeps segments and silence symmetric and
    # guarantees they jointly cover [0, total_frames) without overlap.
    out = []
    for w0, w1 in runs:
        start = w0 * hop
        end = min(w1 * hop, total_frames)
        if end > start:
            out.append(Segment(start, end))
    return out


def detect_levels(
    db_levels: np.ndarray,
    total_frames: int,
    sample_rate: int,
    params: DetectorParams,
) -> Tuple[List[Segment], List[Segment], float]:
    """Build (segments, silence, threshold) from per-window dB levels.

    Silence runs shorter than min_silence_ms are absorbed into the
    surrounding audio and never become cut points.
    """
    params.validate()
    hop = max(1, round(params.hop_ms * sample_rate / 1000.0))
    min_silence_frames = max(1, round(params.min_silence_ms * sample_rate / 1000.0))
    threshold = effective_threshold(db_levels, params)
    runs = silence_window_runs(db_levels, threshold)
    silences = [
        s
        for s in _runs_to_samples(runs, len(db_levels), hop, total_frames)
        if s.length >= min_silence_frames
    ]
    segments: List[Segment] = []
    pos = 0
    for s in silences:
        if s.start_sample > pos:
            segments.append(Segment(pos, s.start_sample))
        pos = max(pos, s.end_sample)
    if pos < total_frames:
        segments.append(Segment(pos, total_frames))
    return segments, silences, threshold


def detect_blocks(
    blocks: Iterable[np.ndarray],
    sample_rate: int,
    channels: int,
    sample_frames: int,
    params: DetectorParams,
) -> DetectionResult:
    """Run detection over a stream of decoded sample blocks."""
    params.validate()
    frame = max(1, round(params.frame_ms * sample_rate / 1000.0))
    hop = max(1, round(params.hop_ms * sample_rate / 1000.0))
    framer = RmsFramer(frame, hop, channels)
    rms: List[float] = []
    for block in blocks:
        rms.extend(framer.feed(block))
    rms.extend(framer.flush())
    db_levels = levels_to_db(rms)
    segments, silences, threshold = detect_levels(
        db_levels, sample_frames, sample_rate, params
    )
    return DetectionResult(
        sample_rate=sample_rate,
        channels=channels,
        sample_frames=sample_frames,
        segments=segments,
        silence=silences,
        threshold_dbfs=threshold,
    )


def detect_array(
    samples: np.ndarray, sample_rate: int, params: DetectorParams
) -> DetectionResult:
    """Convenience wrapper for an in-memory (frames, channels) array."""
    samples = np.asarray(samples, dtype=np.float64)
    if samples.ndim == 1:
        samples = samples.reshape(-1, 1)
    return detect_blocks(
        [samples], sample_rate, samples.shape[1], samples.shape[0], params
    )


def detect_file(path: str, params: DetectorParams) -> DetectionResult:
    """Detect segments of one WAV file, streaming it in bounded chunks."""
    from .wav_reader import iter_chunks, read_info  # local, avoids a cycle

    info = read_info(path)
    blocks = (block for _, block in iter_chunks(path))
    return detect_blocks(
        blocks, info.sample_rate, info.channels, info.sample_frames, params
    )
