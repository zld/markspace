"""Frame-level feature extraction: RMS energy, adaptive noise floor, slopes.

Energy is computed in fixed-size blocks of frames so memory stays bounded for
long recordings: the full float signal is never materialised, only the
current block plus an overlap of one frame is converted.

The noise floor is a *causal* sliding quantile of frame energy. Keeping it
local (rather than using one global value) means a long stretch of steady
low-level air-conditioning hum cannot pull up the threshold used for a quiet
event elsewhere in the recording.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np

# RMS values are clipped above this floor so log-space slopes stay finite.
ENERGY_FLOOR = 1e-12


@dataclass(frozen=True)
class FrameFeatures:
    """Per-frame analysis signals, all indexed by frame number."""

    frame_length: int
    hop_length: int
    sample_rate: int
    energy: np.ndarray  # RMS per frame, float64
    noise_floor: np.ndarray  # sliding quantile of energy
    slope: np.ndarray  # log-energy difference between adjacent frames

    @property
    def n_frames(self) -> int:
        return int(self.energy.shape[0])

    def frame_start_sample(self, frame: int) -> int:
        return frame * self.hop_length

    def frame_end_sample(self, frame: int) -> int:
        return min((frame + 1) * self.hop_length + (self.frame_length - self.hop_length),
                   self._total_samples())

    def _total_samples(self) -> int:
        return self.frame_start_sample(self.n_frames - 1) + self.frame_length


def frame_energy(samples: np.ndarray, frame_length: int, hop_length: int,
                 block_frames: int = 4096) -> np.ndarray:
    """RMS energy per frame, computed in bounded-memory blocks.

    Stereo input yields the mean of the per-channel squared values inside
    each frame, as required by the pipeline spec.
    """
    if frame_length <= 0 or hop_length <= 0 or hop_length > frame_length:
        raise ValueError("frame_length and hop_length must be positive, hop <= frame")

    total = int(samples.shape[0])
    if total < frame_length:
        # Short file: pad with silence up to one full frame so the detector
        # always has something to classify.
        pad = frame_length - total
        if samples.ndim == 1:
            samples = np.concatenate([samples, np.zeros(pad, dtype=samples.dtype)])
        else:
            zeros = np.zeros((pad, samples.shape[1]), dtype=samples.dtype)
            samples = np.concatenate([samples, zeros], axis=0)
        total = frame_length

    n_frames = 1 + (total - frame_length) // hop_length
    out = np.empty(n_frames, dtype=np.float64)
    channels = samples.shape[1] if samples.ndim == 2 else 1
    divisor = float(frame_length * channels)

    start = 0
    while start < n_frames:
        end = min(start + block_frames, n_frames)
        # Gather the block's frames with one extra frame of overlap context;
        # as_strided gives a view, so no signal-sized copy is made.
        first_sample = start * hop_length
        last_sample = (end - 1) * hop_length + frame_length
        block = samples[first_sample:last_sample]
        block_view = np.lib.stride_tricks.sliding_window_view(
            block, window_shape=frame_length, axis=0
        )[::hop_length]
        if samples.ndim == 2:
            squared = block_view.astype(np.float64) ** 2
            mean_sq = squared.mean(axis=(1, 2))
        else:
            mean_sq = (block_view.astype(np.float64) ** 2).mean(axis=1)
        out[start:end] = np.sqrt(np.maximum(mean_sq, 0.0))
        start = end
    return out


def sliding_quantile(values: np.ndarray, window: int, quantile: float) -> np.ndarray:
    """Causal sliding quantile; the window is edge-padded on the left."""
    if window <= 0:
        raise ValueError("window must be positive")
    if not 0.0 <= quantile <= 1.0:
        raise ValueError("quantile must be within [0, 1]")
    n = values.shape[0]
    if n == 0:
        return np.empty(0, dtype=np.float64)
    radius = window // 2
    padded = np.concatenate([np.full(radius, values[0]), values])
    out = np.empty(n, dtype=np.float64)
    for i in range(n):
        lo = i
        hi = min(i + radius + 1, n)
        out[i] = np.quantile(padded[lo:hi], quantile)
    return out


def log_energy_slope(energy: np.ndarray) -> np.ndarray:
    """First difference of log energy; positive = rising (fade-in)."""
    safe = np.maximum(energy, ENERGY_FLOOR)
    log_e = np.log(safe)
    slope = np.diff(log_e)
    return np.concatenate([[0.0], slope])


def extract_features(samples: np.ndarray, sample_rate: int, frame_ms: float,
                     noise_window_ms: float, noise_quantile: float = 0.25) -> FrameFeatures:
    """Build the full frame feature set for one decoded signal."""
    if frame_ms <= 0 or noise_window_ms <= 0:
        raise ValueError("frame_ms and noise_window_ms must be positive")
    frame_length = max(1, int(round(sample_rate * frame_ms / 1000.0)))
    hop_length = frame_length  # non-overlapping frames
    window = max(1, int(round(noise_window_ms / frame_ms)))

    energy = frame_energy(samples, frame_length, hop_length)
    noise_floor = sliding_quantile(energy, window, noise_quantile)
    slope = log_energy_slope(energy)
    return FrameFeatures(
        frame_length=frame_length,
        hop_length=hop_length,
        sample_rate=sample_rate,
        energy=energy,
        noise_floor=noise_floor,
        slope=slope,
    )
