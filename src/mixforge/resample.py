"""Sample-rate conversion and timeline placement.

Only NumPy is used: linear interpolation resampling with a mid-sample time
convention, which makes frame counts deterministic across platforms. Every
track is normalized to the project sample rate before entering the mix graph.
"""

from __future__ import annotations

import numpy as np

from .model import TrackSpec


def resample_track(data: np.ndarray, source_rate: int, target_rate: int) -> np.ndarray:
    """Resample ``data`` (frames x channels) from ``source_rate`` to ``target_rate``.

    Output frame count is derived from the time duration and rounded to the
    nearest sample, matching the convention used for timeline lengths.
    """

    data = np.asarray(data, dtype=np.float64)
    if data.ndim == 1:
        data = data[:, np.newaxis]
    source_frames = data.shape[0]
    if source_rate == target_rate or source_frames == 0:
        return data.copy()
    duration = source_frames / source_rate
    target_frames = int(round(duration * target_rate))
    if target_frames == 0:
        return np.zeros((0, data.shape[1]), dtype=np.float64)
    # Sample k is centred at time (k + 0.5) / rate; map target times back
    # onto source positions so both ends represent the same interval.
    target_times = (np.arange(target_frames, dtype=np.float64) + 0.5) / target_rate
    source_positions = target_times * source_rate - 0.5
    return _interp1d(source_positions, data)


def _interp1d(positions: np.ndarray, data: np.ndarray) -> np.ndarray:
    """Linear interpolation along axis 0, clamping outside the source range."""

    source_frames, channels = data.shape
    index0 = np.floor(positions).astype(np.int64)
    fraction = positions - index0
    left = np.clip(index0, 0, source_frames - 1)
    right = np.clip(index0 + 1, 0, source_frames - 1)
    fraction = fraction[:, np.newaxis]
    return data[left] * (1.0 - fraction) + data[right] * fraction


def project_frame_count(tracks, project_rate: int) -> int:
    """Total mix length in samples, rounded from the latest track end time."""

    end_time = 0.0
    for track in tracks:
        duration = track.frames / track.sample_rate
        end_time = max(end_time, track.offset + duration)
    return int(round(end_time * project_rate))


def place_track(data: np.ndarray, track: TrackSpec, total_frames: int,
                project_rate: int) -> np.ndarray:
    """Place resampled track data on the project timeline at its offset."""

    channels = data.shape[1]
    buffer = np.zeros((total_frames, channels), dtype=np.float64)
    start = int(round(track.offset * project_rate))
    if start >= total_frames:
        return buffer
    end = min(start + data.shape[0], total_frames)
    buffer[start:end, :] = data[: end - start, :]
    return buffer
