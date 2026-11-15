"""Mixing graph: decode -> resample -> automation -> stable DAG summation.

The graph is a fixed two-level DAG: one :class:`TrackNode` per track feeding a
single summation bus.  Track nodes are stored and processed in sorted track-id
order so the floating-point summation order is stable.  Both the offline
engine and the streaming renderer call :meth:`MixGraph.read_block`, which
guarantees bit-identical output whatever block size is used: every stateful
component (file cursor, resampler history, gain-cursor position, output
queues) lives inside the nodes and never depends on block boundaries.
"""

from __future__ import annotations

from pathlib import Path

import numpy as np
import soundfile as sf

from . import AudioProcessError, ConfigError
from .automation import GainCurve
from .model import Project, TrackConfig
from .resample import SincResampler

CHANNELS = 2


class TrackNode:
    """One decoded, resampled and automated track stream."""

    def __init__(self, config: TrackConfig, target_rate: int, project_frames: int):
        self.config = config
        self.target_rate = target_rate
        self.project_frames = project_frames
        self.curve = GainCurve(config.gain, config.automation)
        self._file = None
        self._info = None
        self._resampler: SincResampler | None = None
        # Resampled frames already produced, before queue trimming.
        self._produced = 0
        self._read_cursor = 0
        self._source_done = False
        self._flushed = False
        self._queue = np.empty((0, CHANNELS), dtype=np.float64)

    # ------------------------------------------------------------------ #
    @property
    def track_id(self) -> str:
        return self.config.track_id

    def _open(self) -> None:
        path = Path(self.config.path)
        try:
            self._file = sf.SoundFile(path, mode="r")
        except (OSError, RuntimeError, sf.LibsndfileError) as exc:
            raise AudioProcessError(
                f"track {self.track_id!r}: cannot decode {path}: {exc}"
            ) from exc
        self._info = sf.info(path)
        if self._info.channels not in (1, CHANNELS):
            raise AudioProcessError(
                f"track {self.track_id!r}: unsupported channel count "
                f"{self._info.channels} (only mono or stereo are accepted)"
            )
        self._resampler = SincResampler(self._info.samplerate, self.target_rate)

    def _read_input(self, count: int) -> np.ndarray:
        """Read ``count`` source frames and normalize to stereo float64."""
        remaining = self._info.frames - self._read_cursor
        if count > remaining:
            count = remaining
        if count <= 0:
            return np.empty((0, CHANNELS), dtype=np.float64)
        try:
            data = self._file.read(
                frames=count, dtype="float64", always_2d=True
            )
        except (OSError, RuntimeError, sf.LibsndfileError) as exc:
            raise AudioProcessError(
                f"track {self.track_id!r}: decode failure: {exc}"
            ) from exc
        self._read_cursor += data.shape[0]
        if data.shape[1] == 1:
            data = np.repeat(data, CHANNELS, axis=1)
        return data

    def _pump(self, req_start: int, req_end: int) -> None:
        """Move data from the source file through resample + automation.

        Runs only until the queue covers the requested project window, so
        retained memory scales with block size plus resampler filter history,
        never with the full source file length.
        """
        if self._file is None:
            self._open()
        while not self._source_done:
            q_head = self._produced - self._queue.shape[0]
            q_tail = self.config.start_frame + self._produced
            if q_tail >= req_end or self.config.start_frame + q_head >= req_end:
                return  # queue already covers (or lies past) the request
            if self.config.start_frame >= self.project_frames:
                return
            # Source frames still needed to cover [req_start, req_end).
            want = max(1, req_end - max(q_tail, self.config.start_frame))
            in_block = self._read_input(want)
            if in_block.shape[0] == 0:
                self._source_done = True
                break
            try:
                resampled = self._resampler.process(in_block, max_output=want)
            except AudioProcessError as exc:
                raise AudioProcessError(
                    f"track {self.track_id!r}: resampling failed: {exc}"
                ) from exc
            self._ingest(resampled)
        if self._source_done and not self._flushed:
            try:
                tail = self._resampler.flush()
            except AudioProcessError as exc:
                raise AudioProcessError(
                    f"track {self.track_id!r}: resampling flush failed: {exc}"
                ) from exc
            self._ingest(tail)
            self._flushed = True
            try:
                self._file.close()
            except (OSError, RuntimeError):
                pass

    def _ingest(self, frames: np.ndarray) -> None:
        if frames.ndim == 1:
            frames = frames[:, np.newaxis]
        if frames.shape[1] == 1:
            frames = np.repeat(frames, CHANNELS, axis=1)
        if frames.shape[0] == 0:
            return
        gains = self.curve.sample_frames(
            self.config.start_frame + self._produced, frames.shape[0]
        )
        self._produced += frames.shape[0]
        frames = frames * gains[:, np.newaxis]
        self._queue = np.concatenate((self._queue, frames), axis=0)

    def read_project_frames(self, start: int, count: int) -> np.ndarray:
        """Return the track's contribution for project frames [start, start+count)."""
        self._pump(start, start + count)
        out = np.zeros((count, CHANNELS), dtype=np.float64)
        q_head = self._produced - self._queue.shape[0]
        lo = start - self.config.start_frame - q_head
        hi = lo + count
        src_lo = max(0, lo)
        src_hi = min(self._queue.shape[0], hi)
        if src_hi > src_lo:
            dst_lo = src_lo - lo
            out[dst_lo:dst_lo + (src_hi - src_lo)] = self._queue[src_lo:src_hi]
        # Drop queue prefix that can never be requested again.
        drop = max(0, min(hi, self._queue.shape[0]))
        if drop:
            self._queue = self._queue[drop:]
        return out

    def close(self) -> None:
        if self._file is not None:
            try:
                self._file.close()
            except (OSError, RuntimeError):
                pass
            self._file = None


class MixGraph:
    """The complete DAG of track nodes plus the summation bus."""

    def __init__(self, project: Project):
        self.project = project
        self.nodes = [
            TrackNode(tc, project.sample_rate, project.frames)
            for tc in sorted(project.tracks, key=lambda t: t.track_id)
        ]
        self._position = 0
        self._closed = False

    @property
    def position(self) -> int:
        return self._position

    def read_block(self, max_frames: int) -> np.ndarray:
        """Process the next block; shorter final block, empty array at EOF."""
        if max_frames <= 0:
            raise ConfigError("block size must be positive")
        remaining = self.project.frames - self._position
        count = min(max_frames, remaining)
        if count == 0:
            return np.empty((0, CHANNELS), dtype=np.float64)
        mix = np.zeros((count, CHANNELS), dtype=np.float64)
        start = self._position
        for node in self.nodes:
            mix += node.read_project_frames(start, count)
        self._position += count
        return mix

    def close(self) -> None:
        if not self._closed:
            for node in self.nodes:
                node.close()
            self._closed = True

    def __enter__(self) -> "MixGraph":
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        self.close()
