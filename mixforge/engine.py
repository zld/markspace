"""Offline render driver: pull blocks from the graph, write WAV atomically.

The output file is written to a sibling temporary file and atomically
renamed over the target only after the last frame is encoded, so a
failed render never leaves a half-written artifact at the target path.
"""

from __future__ import annotations

import os
from pathlib import Path

import numpy as np
import soundfile as sf

from .errors import MediaError
from .graph import MixGraph
from .metrics import ClipMeter, TrackMeter
from .stream import StreamExecutor

OFFLINE_BLOCK = 8192


class RenderStats:
    def __init__(self, track_meters: list[TrackMeter], clip: ClipMeter, frames: int):
        self.track_meters = track_meters
        self.clip = clip
        self.frames = frames


def _drive(executor: StreamExecutor, sink) -> RenderStats:
    graph = executor.graph
    meters = [TrackMeter(t.name) for t in graph.tracks]
    clip = ClipMeter()
    while True:
        step = executor.next_block()
        if step is None:
            break
        mix, contribs = step
        for meter, contrib in zip(meters, contribs):
            meter.update(contrib)
        clip.update(mix)
        sink(mix)
    return RenderStats(meters, clip, graph.frames)


def render_to_file(graph: MixGraph, out_path: str | Path, block_frames: int) -> RenderStats:
    """Render the graph to a float WAV at ``out_path`` (atomic replace)."""
    out_path = Path(out_path)
    tmp = out_path.with_name(f"{out_path.name}.tmp-{os.getpid()}")
    try:
        with sf.SoundFile(
            str(tmp), "w", samplerate=graph.target_rate, channels=2,
            subtype="FLOAT", format="WAV",
        ) as fh:
            stats = _drive(StreamExecutor(graph, block_frames), fh.write)
    except MediaError:
        _unlink_quietly(tmp)
        raise
    except Exception as exc:
        _unlink_quietly(tmp)
        raise MediaError(f"cannot write output {out_path}: {exc}") from exc
    try:
        os.replace(tmp, out_path)
    except OSError as exc:
        _unlink_quietly(tmp)
        raise MediaError(f"cannot move {tmp} into place at {out_path}: {exc}") from exc
    return stats


def render_to_buffer(graph: MixGraph, block_frames: int) -> tuple[np.ndarray, RenderStats]:
    """Render the graph into memory (used by the streaming parity check)."""
    chunks: list[np.ndarray] = []
    stats = _drive(StreamExecutor(graph, block_frames), chunks.append)
    data = np.concatenate(chunks, axis=0) if chunks else np.zeros((0, 2))
    return data, stats


def _unlink_quietly(path: Path) -> None:
    try:
        os.unlink(path)
    except OSError:
        pass
