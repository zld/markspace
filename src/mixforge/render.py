"""Offline and streaming renderers sharing one :class:`MixGraph`.

Both paths iterate the same block boundaries and call the same graph nodes;
only the buffering strategy differs.  Outputs land via a temp file plus
atomic replace, and any failure cleans up unfinished artifacts.
"""

from __future__ import annotations

import os
import tempfile
from pathlib import Path

import numpy as np
import soundfile as sf

from .errors import AssetError, ClipPolicyError
from .mixer import MixGraph
from .project import Project


def _atomic_write_wav(path: Path, data: np.ndarray, rate: int) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(prefix=f".{path.name}.", suffix=".tmp", dir=path.parent)
    os.close(fd)
    try:
        sf.write(tmp_name, data, rate, subtype="FLOAT", format="WAV")
        os.replace(tmp_name, path)
    except (RuntimeError, OSError, ValueError) as exc:
        _safe_unlink(tmp_name)
        raise AssetError(f"cannot write output {path}: {exc}") from exc


def _safe_unlink(name: str) -> None:
    try:
        os.unlink(name)
    except OSError:
        pass


def _apply_policy(graph: MixGraph, data: np.ndarray) -> np.ndarray:
    policy = graph.project.clip_policy
    threshold = graph.project.clip_threshold
    if policy == "hard_limit":
        return np.clip(data, -threshold, threshold)
    if policy == "error" and graph.bus_stats.clip_count > 0:
        raise ClipPolicyError(
            f"clipping detected: {graph.bus_stats.clip_count} sample(s) over "
            f"threshold {threshold}")
    return data


def render_offline(graph: MixGraph, block_frames: int) -> np.ndarray:
    """Render the whole timeline in blocks, then write one WAV atomically."""
    project = graph.project
    chunks = []
    for start in range(0, project.frames, block_frames):
        count = min(block_frames, project.frames - start)
        chunks.append(graph.process_block(start, count))
    output = np.vstack(chunks) if chunks else np.zeros((0, project.channels))
    output = _apply_policy(graph, output)
    _atomic_write_wav(project.output, output, project.project_rate)
    return output


def render_stream(graph: MixGraph, block_frames: int, *,
                  fault_after_blocks: int | None = None) -> np.ndarray:
    """Render block by block, streaming each block into a temp WAV.

    ``fault_after_blocks`` exists for tests: it simulates an interrupted
    stream writer and must leave no finished artifact behind.
    """
    project = graph.project
    output = project.output
    output.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(prefix=f".{output.name}.", suffix=".tmp",
                                    dir=output.parent)
    os.close(fd)
    written = 0
    try:
        with sf.SoundFile(tmp_name, mode="w", samplerate=project.project_rate,
                          channels=project.channels, subtype="FLOAT",
                          format="WAV") as sink:
            for block_index, start in enumerate(
                    range(0, project.frames, block_frames)):
                if fault_after_blocks is not None and block_index >= fault_after_blocks:
                    raise OSError("simulated stream write interruption")
                count = min(block_frames, project.frames - start)
                block = graph.process_block(start, count)
                sink.write(block)
                written += count
        # Policy check happens before the atomic replace: an error policy
        # failure must never publish an output file.
        data = np.empty((written, project.channels), dtype=np.float64)
        with sf.SoundFile(tmp_name, mode="r") as source:
            source.read(out=data)
        data = _apply_policy(graph, data)
        if project.clip_policy == "hard_limit":
            # Re-write the limited stream before publishing.
            _safe_unlink(tmp_name)
            sf.write(tmp_name, data, project.project_rate, subtype="FLOAT",
                     format="WAV")
        os.replace(tmp_name, output)
        return data
    except ClipPolicyError:
        _safe_unlink(tmp_name)
        raise
    except (RuntimeError, OSError, ValueError) as exc:
        _safe_unlink(tmp_name)
        raise AssetError(f"stream render failed for {output}: {exc}") from exc
