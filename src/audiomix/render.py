"""Offline and chunked rendering driven by the same block iterator.

Both modes instantiate :class:`BlockRenderer` and pull fixed-size output
blocks from it; the only difference is the block length.  Track source
files are streamed (one read chunk at a time) and resampler state is one
carry frame, so memory stays bounded regardless of total timeline length.
"""
from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

import numpy as np
import soundfile as sf

from .mixer import MixGraph
from .model import RenderError, Timeline, TrackSpec
from .resample import (LinearResampler, frame_offset, map_channels,
                       resampled_frame_count)

_READ_CHUNK = 8192


@dataclass
class SourceInfo:
    frames: int
    sample_rate: int
    channels: int


def probe_source(path: Path) -> SourceInfo:
    try:
        info = sf.info(str(path))
    except RuntimeError as exc:
        raise RenderError(f"cannot read WAV source: {path} ({exc})") from exc
    if info.frames < 0:
        raise RenderError(f"corrupt or unreadable WAV source: {path}")
    return SourceInfo(int(info.frames), int(info.samplerate), int(info.channels))


class TrackPlayer:
    """Streams one source through mapping and resampling onto the timeline.

    ``_position`` is the absolute timeline frame index of the first queued
    resampled frame; frames before the track start are skipped but never
    silently dropped.  Frames before the track start and after its resampled
    tail are rendered as silence.
    """

    def __init__(self, spec: TrackSpec, track_index: int,
                 timeline: Timeline):
        self.spec = spec
        self.index = track_index
        self.rate = timeline.output_rate
        self.channels = timeline.channels
        path = spec.file
        if not path.is_absolute():
            path = timeline.base_dir / path
        self.path = path
        info = probe_source(self.path)
        self.info = info
        self.start_frame = frame_offset(spec.start, self.rate)
        self.resampled_total = resampled_frame_count(
            info.frames, info.sample_rate, self.rate)
        self.end_frame = self.start_frame + self.resampled_total
        self._reader: sf.SoundFile | None = None
        self._resampler = LinearResampler(info.sample_rate, self.rate)
        self._eof = False
        self._queue = np.zeros((0, self.channels), dtype=np.float64)
        self._position = self.start_frame

    def _open(self) -> sf.SoundFile:
        if self._reader is None:
            try:
                self._reader = sf.SoundFile(str(self.path), mode="r")
            except RuntimeError as exc:
                raise RenderError(
                    f"cannot open WAV source {self.path}: {exc}",
                    track=self.spec.name) from exc
        return self._reader

    def _close(self) -> None:
        if self._reader is not None:
            self._reader.close()
            self._reader = None

    def _pull_input(self) -> None:
        """Read one source chunk, map channels and resample into the queue."""
        if self._eof:
            return
        reader = self._open()
        try:
            raw = reader.read(frames=_READ_CHUNK, dtype="float64",
                              always_2d=True)
        except RuntimeError as exc:
            raise RenderError(
                f"failed reading {self.path}: {exc}",
                track=self.spec.name) from exc
        if raw.shape[0] == 0:
            tail = self._resampler.flush()
            if tail.size:
                self._queue = np.vstack((self._queue, tail))
            self._eof = True
            self._close()
            return
        mapped = map_channels(raw, self.spec.channel_map, self.channels)
        self._queue = np.vstack(
            (self._queue, self._resampler.process(mapped)))

    def _ensure(self, up_to_exclusive: int) -> None:
        """Guarantee queue covers every timeline frame below *up_to*."""
        while not self._eof and self._position + self._queue.shape[0] < \
                up_to_exclusive:
            self._pull_input()

    def read_block(self, frame_start: int, count: int) -> np.ndarray:
        """Return exactly *count* output frames starting at *frame_start*."""
        out = np.zeros((count, self.channels), dtype=np.float64)
        if count == 0:
            return out
        block_end = frame_start + count
        overlap_lo = max(frame_start, self.start_frame)
        overlap_hi = min(block_end, self.end_frame)
        if overlap_lo >= overlap_hi:
            return out
        self._ensure(overlap_hi)
        queue_end = self._position + self._queue.shape[0]
        if queue_end < overlap_hi:
            raise RenderError(
                f"unexpected end of source for track {self.spec.name!r} "
                f"(needed through frame {overlap_hi}, have {queue_end})",
                track=self.spec.name)
        lo = overlap_lo - self._position
        hi = overlap_hi - self._position
        out[overlap_lo - frame_start:overlap_hi - frame_start] = \
            self._queue[lo:hi]
        # Consume only frames up to the block end; later queued frames stay
        # buffered for subsequent blocks.
        consumed = min(max(0, block_end - self._position),
                       self._queue.shape[0])
        if consumed > 0:
            self._queue = self._queue[consumed:]
            self._position += consumed
        return out

    def close(self) -> None:
        if self._reader is not None:
            self._reader.close()
            self._reader = None


class BlockRenderer:
    """Renders the whole timeline as a sequence of fixed-size blocks.

    The same code path serves offline and streaming modes: block size alone
    differs, so results are bit-identical across chunkings.
    """

    def __init__(self, timeline: Timeline, block_frames: int):
        if block_frames <= 0:
            raise RenderError(
                f"block-frames must be positive, got {block_frames}")
        self.timeline = timeline
        self.block_frames = int(block_frames)
        self.graph = MixGraph(timeline)
        self.players = [TrackPlayer(t, i, timeline)
                        for i, t in enumerate(timeline.tracks)]
        self.total_frames = max(
            (p.end_frame for p in self.players), default=0)
        self._cursor = 0
        self._closed = False

    def __iter__(self):
        return self

    def __next__(self) -> tuple[int, np.ndarray]:
        if self._cursor >= self.total_frames:
            self.close()
            raise StopIteration
        start = self._cursor
        count = min(self.block_frames, self.total_frames - start)
        bus = np.zeros((count, self.timeline.channels), dtype=np.float64)
        for node_index, player in enumerate(self.players):
            raw = player.read_block(start, count)
            processed = self.graph.process_track_block(
                node_index, raw, start)
            bus += processed
        block = self.graph.mix_tail(bus, start)
        self._cursor += count
        return start, block

    def close(self) -> None:
        if not self._closed:
            for player in self.players:
                player.close()
            self._closed = True


def render_to_file(timeline: Timeline, out_path: str | Path,
                   block_frames: int, limit: bool = False) -> int:
    """Render *timeline* to a float32 PCM WAV via the block iterator.

    Float output preserves summation precision.  When *limit* is true,
    samples beyond [-1, 1] are hard-clipped before writing; the caller
    collects raw (pre-limiter) clipping counts from the blocks.  Output is
    written to a temporary file and atomically renamed, so a failed render
    never leaves a half-written target behind.
    """
    out_path = Path(out_path)
    out_path.parent.mkdir(parents=True, exist_ok=True)
    tmp_path = out_path.with_name(out_path.name + ".part")
    writer: sf.SoundFile | None = None
    try:
        renderer = BlockRenderer(timeline, block_frames)
        writer = sf.SoundFile(
            str(tmp_path), mode="w", samplerate=timeline.output_rate,
            channels=timeline.channels, subtype="FLOAT", format="WAV")
        for _start, block in renderer:
            if limit:
                np.clip(block, -1.0, 1.0, out=block)
            writer.write(block.astype(np.float32, copy=False))
        total = renderer.total_frames
        renderer.close()
        writer.close()
        writer = None
        tmp_path.replace(out_path)
        return total
    except (OSError, RuntimeError) as exc:
        if writer is not None:
            writer.close()
        if tmp_path.exists():
            tmp_path.unlink(missing_ok=True)
        raise RenderError(f"failed writing output {out_path}: {exc}") from exc
    except RenderError:
        if writer is not None:
            writer.close()
        if tmp_path.exists():
            tmp_path.unlink(missing_ok=True)
        raise
