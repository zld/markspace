"""Individual mix-graph nodes: source, resample, channel map, gain and bus.

Every node processes complete blocks and keeps no hidden cross-block state
beyond what its class documents, which lets the offline and streaming
renderers drive the exact same node implementations.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np
import soundfile as sf

from .automation import AutomationCurve, db_to_linear, gain_at
from .model import ManifestError, check_duration
from .resample import StreamingResampler


class AudioIOError(RuntimeError):
    """Raised for unreadable, empty, corrupt or unsupported audio files."""


class ConsistencyError(RuntimeError):
    """Raised when offline and streaming renders disagree."""


def _open_error(path: str, track_name: str, exc: Exception) -> AudioIOError:
    return AudioIOError(f"track {track_name!r}: cannot read audio file {path}: {exc}")


@dataclass(frozen=True)
class WavInfo:
    frames: int
    sample_rate: int
    channels: int


class SourceNode:
    """Read-only WAV source with validated mono/stereo layout."""

    def __init__(self, path: str, track_name: str):
        self.path = path
        self.track_name = track_name
        try:
            self._file = sf.SoundFile(path, mode="r")
        except Exception as exc:  # soundfile raises several backend variants
            raise _open_error(path, track_name, exc) from exc
        try:
            self.info = WavInfo(
                frames=int(self._file.frames),
                sample_rate=int(self._file.samplerate),
                channels=int(self._file.channels),
            )
            self._validate()
        except Exception as exc:
            self.close()
            if isinstance(exc, (AudioIOError, ConsistencyError, ManifestError)):
                raise
            raise _open_error(path, track_name, exc) from exc

    def _validate(self) -> None:
        if self.info.frames <= 0:
            raise AudioIOError(f"track {self.track_name!r}: {self.path}: audio file is empty")
        if self.info.sample_rate <= 0:
            raise AudioIOError(f"track {self.track_name!r}: {self.path}: invalid sample rate")
        if self.info.channels not in (1, 2):
            raise AudioIOError(
                f"track {self.track_name!r}: {self.path}: unsupported channel count "
                f"{self.info.channels} (mono or stereo only)"
            )
        check_duration(self.info.frames, self.info.sample_rate, self.track_name)

    @property
    def channels(self) -> int:
        return self.info.channels

    @property
    def sample_rate(self) -> int:
        return self.info.sample_rate

    @property
    def frames(self) -> int:
        return self.info.frames

    def read(self, count: int) -> np.ndarray:
        """Read up to ``count`` frames, returning ``(channels, n)``."""
        try:
            data = self._file.read(count, dtype="float64", always_2d=True)
        except Exception as exc:
            raise _open_error(self.path, self.track_name, exc) from exc
        if data.size == 0:
            return np.zeros((self.info.channels, 0), dtype=np.float64)
        return np.ascontiguousarray(data.T, dtype=np.float64)

    def close(self) -> None:
        try:
            self._file.close()
        except Exception:
            pass

    def __enter__(self) -> "SourceNode":
        return self

    def __exit__(self, *exc_info) -> None:
        self.close()


class ResampleNode:
    """Stateful resampler; pass-through when rates already match."""

    def __init__(self, rate_in: int, rate_out: int, channels: int):
        self.rate_in = rate_in
        self.rate_out = rate_out
        self.channels = channels
        self.passthrough = rate_in == rate_out
        self.rim = 0
        self._resampler: StreamingResampler | None = None
        if not self.passthrough:
            self._resampler = StreamingResampler(rate_in, rate_out, channels)
            self.rim = self._resampler.rim

    def process(self, block: np.ndarray) -> np.ndarray:
        if self.passthrough:
            return np.asarray(block, dtype=np.float64)
        assert self._resampler is not None
        return self._resampler.pull(block)

    def finalize(self) -> np.ndarray:
        if self._resampler is None:
            return np.zeros((self.channels, 0), dtype=np.float64)
        return self._resampler.finalize()


class ChannelMapNode:
    """Map mono/stereo input to the fixed stereo bus with an optional balance.

    Panning is attenuation-only: centre passes both channels at unity and the
    extremes mute the opposite side, so no pan value can raise a track above
    its source level.
    """

    def __init__(self, channels: int, pan: float = 0.0):
        if channels not in (1, 2):
            raise ValueError("only mono and stereo sources are supported")
        self.channels = channels
        self.pan = float(pan)
        self.left_gain = 1.0 - max(self.pan, 0.0)
        self.right_gain = 1.0 + min(self.pan, 0.0)

    def process(self, block: np.ndarray) -> np.ndarray:
        if block.ndim != 2 or block.shape[0] != self.channels:
            raise ValueError("channel-map input has the wrong layout")
        if self.channels == 1:
            mono = block[0]
            return np.vstack([mono * self.left_gain, mono * self.right_gain])
        out = np.empty((2, block.shape[1]), dtype=np.float64)
        out[0] = block[0] * self.left_gain
        out[1] = block[1] * self.right_gain
        return out


class AutomationGainNode:
    """Apply a constant dB offset plus a piecewise-linear automation curve."""

    def __init__(self, gain_db: float, curve: AutomationCurve):
        self.gain_db = float(gain_db)
        self.curve = curve

    def process(self, block: np.ndarray, start_frame: int) -> np.ndarray:
        count = block.shape[1]
        if count == 0:
            return block
        frames = start_frame + np.arange(count, dtype=np.int64)
        db = gain_at(self.curve, frames) + self.gain_db
        return block * db_to_linear(db)[None, :]


class BusNode:
    """Stereo summing bus: counts pre-limiter overs, then clips to [-1, 1].

    The node consumes one block per call so both renderers can stream to disk
    without retaining the entire programme in memory.
    """

    def __init__(self, frames_total: int):
        self.frames_total = int(frames_total)
        self.clip_count = 0
        self.frames_seen = 0

    def mix_block(self, blocks: list[np.ndarray], count: int) -> np.ndarray:
        """Sum per-track blocks, count overs before limiting, then clamp."""
        if count <= 0:
            raise ValueError("bus block must contain at least one frame")
        if self.frames_seen + count > self.frames_total:
            raise ValueError("bus block exceeds the project frame clock")
        total = np.zeros((2, count), dtype=np.float64)
        for block in blocks:
            if block.shape != total.shape:
                raise ValueError("track block shape does not match the bus block")
            total += block
        if total.size:
            self.clip_count += int(np.count_nonzero(np.abs(total) > 1.0))
        np.clip(total, -1.0, 1.0, out=total)
        self.frames_seen += count
        return total

    def finish(self) -> int:
        if self.frames_seen != self.frames_total:
            raise ConsistencyError(
                f"bus produced {self.frames_seen} frames, expected {self.frames_total}"
            )
        return self.clip_count
