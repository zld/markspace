"""Offline render engine: drive the graph and write a float WAV mix.

The engine never holds the whole mix in memory: graph blocks are streamed
straight into a temporary WAV file next to the destination.  The file is
renamed into place only after every frame has been written; any failure
removes the temporary file so no half-written output is left behind and no
success report is emitted.
"""

from __future__ import annotations

import os
from pathlib import Path

import numpy as np
import soundfile as sf

from . import AudioProcessError, WriteError
from .graph import CHANNELS, MixGraph
from .metrics import MetricsAccumulator, summarize_track


def write_float_wav(
    path: str | Path,
    graph: MixGraph,
    block_frames: int,
    sample_frames: tuple[int, ...] = (),
) -> dict:
    """Render ``graph`` to ``path`` and return the offline report fragment."""
    destination = Path(path)
    destination.parent.mkdir(parents=True, exist_ok=True)
    tmp_path = destination.with_name(
        f".{destination.name}.tmp-{os.getpid()}"
    )
    mix_metrics = MetricsAccumulator()
    frames_written = 0
    writer = None
    try:
        try:
            writer = sf.SoundFile(
                tmp_path,
                mode="w",
                samplerate=graph.project.sample_rate,
                channels=CHANNELS,
                subtype="FLOAT",
                format="WAV",
            )
            while True:
                block = graph.read_block(block_frames)
                if block.shape[0] == 0:
                    break
                writer.write(block.astype(np.float32, copy=False))
                mix_metrics.add_block(block)
                frames_written += block.shape[0]
            writer.close()
            writer = None
        except (OSError, RuntimeError, sf.LibsndfileError) as exc:
            raise WriteError(f"failed to write {destination}: {exc}") from exc
        if frames_written != graph.project.frames:
            raise AudioProcessError(
                f"rendered {frames_written} frames but project requires "
                f"{graph.project.frames}"
            )
        try:
            os.replace(tmp_path, destination)
        except OSError as exc:
            raise WriteError(
                f"failed to finalize {destination}: {exc}"
            ) from exc
    except BaseException:
        if writer is not None:
            try:
                writer.close()
            except (OSError, RuntimeError):
                pass
        try:
            tmp_path.unlink(missing_ok=True)
        except OSError:
            pass
        raise

    tracks = [
        summarize_track(
            node.track_id, node.accumulator, node.curve, sample_frames
        )
        for node in graph.nodes
    ]
    return {
        "frames": frames_written,
        "clip_samples": mix_metrics.clip_samples,
        "mix_peak": mix_metrics.peak,
        "mix_rms": mix_metrics.rms,
        "tracks": tracks,
    }
