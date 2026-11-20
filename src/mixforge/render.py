"""Offline and streaming render entry points.

Both modes drive the same :class:`~mixforge.graph.MixGraph` block
processing; they differ only in how the master output is collected:

- ``offline``: all blocks are gathered in memory, then written at once.
- ``stream``: blocks are written to the output file as they are produced
  and consumed source cache is released, bounding memory.

In both cases the output is first written to a temporary file next to the
target and atomically moved into place on success; on failure the
temporary file is removed.
"""

from __future__ import annotations

import os
from pathlib import Path

import numpy as np
import soundfile as sf

from .automation import GainCurve
from .graph import build_graph
from .metrics import StatsAccumulator
from .model import AudioIOError, Project


def _atomic_replace(tmp_path: Path, out_path: Path) -> None:
    os.replace(tmp_path, out_path)


def _write_whole(tmp_path: Path, data: np.ndarray, sample_rate: int) -> None:
    try:
        sf.write(
            str(tmp_path), data, sample_rate, format="WAV", subtype="FLOAT"
        )
    except Exception as exc:
        raise AudioIOError(f"cannot write {tmp_path}: {exc}") from exc


class _StreamWriter:
    def __init__(self, tmp_path: Path, sample_rate: int) -> None:
        try:
            self._sf = sf.SoundFile(
                str(tmp_path), "w",
                samplerate=sample_rate, channels=2,
                format="WAV", subtype="FLOAT",
            )
        except Exception as exc:
            raise AudioIOError(f"cannot open {tmp_path} for writing: {exc}") from exc

    def write(self, block: np.ndarray) -> None:
        try:
            self._sf.write(block)
        except Exception as exc:
            raise AudioIOError(f"failed writing audio block: {exc}") from exc

    def close(self) -> None:
        self._sf.close()


def _curve_samples(
    project: Project, sample_rate: int, duration: float, step_ms: float
) -> dict[str, list[dict]]:
    """Sample each track's gain curve on a time grid plus its keyframes."""
    step_s = step_ms / 1000.0
    grid: list[float] = []
    t = 0.0
    while t <= duration + 1e-12:
        grid.append(round(t, 9))
        t += step_s
    samples: dict[str, list[dict]] = {}
    for track in project.tracks:
        curve = GainCurve(track.keyframes)
        times = set(grid)
        for kf in track.keyframes:
            absolute = track.start + kf.time
            if 0.0 <= absolute <= duration + 1e-9:
                times.add(round(absolute, 9))
        points = []
        for absolute in sorted(times):
            local = absolute - track.start
            db = float(curve.db_at(local))
            frame = absolute * sample_rate
            points.append(
                {
                    "frame": int(frame) if float(frame).is_integer() else frame,
                    "db": db,
                    "gain": float(10.0 ** (db / 20.0)),
                }
            )
        samples[track.track_id] = points
    return samples


def render(
    project: Project,
    out_path: str | Path,
    sample_rate: int,
    duration: float,
    mode: str,
    block_size: int,
    curve_step_ms: float = 100.0,
) -> dict:
    """Render the project and return the JSON-serializable report."""
    if sample_rate <= 0:
        raise ValueError("sample_rate must be positive")
    if duration <= 0:
        raise ValueError("duration must be positive")
    if block_size <= 0:
        raise ValueError("block_size must be positive")
    if mode not in ("offline", "stream"):
        raise ValueError(f"unknown render mode {mode!r}")

    total_frames = int(round(duration * sample_rate))
    graph = build_graph(project, sample_rate, total_frames)
    track_stats = {t.track_id: StatsAccumulator() for t in project.tracks}
    mix_stats = StatsAccumulator()

    out_path = Path(out_path)
    if out_path.parent and not out_path.parent.exists():
        out_path.parent.mkdir(parents=True, exist_ok=True)
    tmp_path = out_path.with_name(f".{out_path.name}.tmp-{os.getpid()}")

    collected: list[np.ndarray] = []
    writer: _StreamWriter | None = None
    try:
        if mode == "stream":
            writer = _StreamWriter(tmp_path, sample_rate)
        for start in range(0, total_frames, block_size):
            count = min(block_size, total_frames - start)
            outputs = graph.execute(start, count)
            for track in project.tracks:
                track_stats[track.track_id].update(outputs[f"gain:{track.track_id}"])
            master = outputs["master"]
            mix_stats.update(master)
            if mode == "offline":
                collected.append(master)
            else:
                assert writer is not None
                writer.write(master)
                graph.release_before(start + count)
        if writer is not None:
            writer.close()
        if mode == "offline":
            data = (
                np.concatenate(collected, axis=0)
                if collected
                else np.zeros((0, 2), dtype=np.float64)
            )
            _write_whole(tmp_path, data, sample_rate)
        _atomic_replace(tmp_path, out_path)
    except Exception:
        if writer is not None:
            writer.close()
        tmp_path.unlink(missing_ok=True)
        raise

    curves = _curve_samples(project, sample_rate, duration, curve_step_ms)
    return {
        "sample_rate": sample_rate,
        "frames": total_frames,
        "channels": 2,
        "mode": mode,
        "tracks": [
            {
                "id": track.track_id,
                **track_stats[track.track_id].report(),
                "curve": curves[track.track_id],
            }
            for track in project.tracks
        ],
        "mix": mix_stats.report(),
    }
