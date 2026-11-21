"""Per-track signal chain: Source -> Resample -> ChannelMap -> AutomationGain.

The offline and streaming renderers both instantiate :class:`TrackGraph`;
only the block sizes they pull differ.
"""

from __future__ import annotations

import math
from dataclasses import dataclass, field

import numpy as np

from .automation import AutomationCurve
from .model import TARGET_SAMPLE_RATE, resampled_frames
from .nodes import (
    AudioIOError,
    AutomationGainNode,
    ChannelMapNode,
    ConsistencyError,
    ResampleNode,
    SourceNode,
    WavInfo,
)

READ_BLOCK_DEFAULT = 4096
# Reading at least this many source frames per fill keeps tiny processing
# chunks (even one frame) from paying resampler setup per output frame.
READ_AHEAD_MIN = 1024

__all__ = [
    "AudioIOError",
    "ConsistencyError",
    "READ_AHEAD_MIN",
    "READ_BLOCK_DEFAULT",
    "TrackGraph",
    "WavInfo",
]


@dataclass
class TrackGraph:
    """One track's node chain plus its pull buffer.

    ``read_block`` controls how many source frames are read per internal read.
    The offline path uses a large block while the streaming path uses the
    caller's chunk size; everything downstream is identical, so any output
    difference would indicate state leaking across block boundaries.
    """

    name: str
    source: SourceNode
    resampler: ResampleNode
    channel_map: ChannelMapNode
    gain: AutomationGainNode
    frames_out: int
    read_block: int
    _chunks: list[np.ndarray] = field(default_factory=list)
    _chunk_offset: int = 0
    _queued: int = 0
    _produced: int = 0
    _source_done: bool = False
    _finalized: bool = False

    @classmethod
    def create(cls, name: str, path: str, pan: float, gain_db: float,
               curve: AutomationCurve,
               read_block: int = READ_BLOCK_DEFAULT) -> "TrackGraph":
        if read_block <= 0:
            raise ValueError("read_block must be positive")
        source = SourceNode(path, name)
        try:
            resampler = ResampleNode(source.sample_rate, TARGET_SAMPLE_RATE, source.channels)
            frames_out = resampled_frames(source.frames, source.sample_rate)
            channel_map = ChannelMapNode(source.channels, pan)
        except Exception:
            source.close()
            raise
        return cls(
            name=name,
            source=source,
            resampler=resampler,
            channel_map=channel_map,
            gain=AutomationGainNode(gain_db, curve),
            frames_out=frames_out,
            read_block=read_block,
        )

    @property
    def sample_rate_in(self) -> int:
        return self.source.sample_rate

    def _append(self, block: np.ndarray) -> None:
        block = self.channel_map.process(block)
        block = self.gain.process(block, self._produced)
        self._produced += block.shape[1]
        if self._produced > self.frames_out:
            raise ConsistencyError(
                f"track {self.name!r}: produced {self._produced} frames, "
                f"expected {self.frames_out}"
            )
        self._chunks.append(block)
        self._queued += block.shape[1]

    def _fill(self, needed: int) -> None:
        while self._queued < needed and not self._finalized:
            missing = needed - self._queued
            if self.resampler.passthrough:
                read_count = missing
            else:
                ratio = self.resampler.rate_out / self.resampler.rate_in
                read_count = int(math.ceil(missing / ratio)) + 2 * self.resampler.rim + 8
            read_count = max(read_count, READ_AHEAD_MIN)
            read_count = min(read_count, max(1, self.read_block))
            if self._source_done:
                tail = self.resampler.finalize()
                self._finalized = True
                if tail.shape[1]:
                    self._append(tail)
                break
            block = self.source.read(read_count)
            if block.shape[1] == 0:
                self._source_done = True
                continue
            out = self.resampler.process(block)
            if out.shape[1]:
                self._append(out)

        if self._finalized and self._produced != self.frames_out:
            raise ConsistencyError(
                f"track {self.name!r}: final length {self._produced} "
                f"!= {self.frames_out}"
            )

    def pull(self, count: int) -> tuple[np.ndarray, int]:
        """Return exactly ``count`` stereo frames plus the number of real frames."""
        if count < 0:
            raise ValueError("frame count must be non-negative")
        if count == 0:
            return np.zeros((2, 0), dtype=np.float64), 0
        self._fill(count)
        take = min(count, self._queued)
        out = np.zeros((2, count), dtype=np.float64)
        remaining = take
        while remaining > 0:
            head = self._chunks[0]
            available = head.shape[1] - self._chunk_offset
            used = min(available, remaining)
            start = self._chunk_offset
            out[:, take - remaining:take - remaining + used] = head[:, start:start + used]
            remaining -= used
            self._chunk_offset += used
            if self._chunk_offset >= head.shape[1]:
                self._chunks.pop(0)
                self._chunk_offset = 0
        self._queued -= take
        return out, take

    def close(self) -> None:
        self.source.close()
