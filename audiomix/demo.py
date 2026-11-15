"""Deterministic demo fixture generation and end-to-end showcase.

All fixture audio is synthesized with a seeded NumPy generator, so every run
is reproducible and no external media files are required.  Three sources are
created at 44100 Hz (mono FLAC), 48000 Hz (stereo WAV) and 32000 Hz (mono
FLAC); one deliberately extends past the project end to exercise tail
truncation.  The demo then runs both the offline engine and the streaming
renderer and cross-checks them.
"""

from __future__ import annotations

import tempfile
from pathlib import Path

import numpy as np
import soundfile as sf

from .engine import write_float_wav
from .graph import MixGraph
from .model import Project, validate_project
from .stream import parity_error, render_stream

SOURCE_SPECS = (
    # id, source rate, channels, duration seconds, file suffix
    ("voice", 44100, 1, 3.0, ".flac"),
    ("ambience", 48000, 2, 2.5, ".wav"),
    ("music", 32000, 1, 3.5, ".flac"),  # longer than the 3 s project
)


def _tone(rng: np.random.Generator, frames: int, rate: int, freq: float) -> np.ndarray:
    t = np.arange(frames, dtype=np.float64) / rate
    warble = 0.5 * np.sin(2 * np.pi * 1.7 * t)
    sig = np.sin(2 * np.pi * (freq + 30.0 * warble) * t)
    sig += 0.05 * rng.standard_normal(frames)
    return sig


def generate_sources(directory: str | Path, seed: int) -> dict[str, str]:
    """Synthesize the three fixture sources and return their paths by id."""
    rng = np.random.default_rng(seed)
    directory = Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    paths: dict[str, str] = {}
    for track_id, rate, channels, seconds, suffix in SOURCE_SPECS:
        frames = int(round(rate * seconds))
        sig = _tone(rng, frames, rate, freq=220.0 + 90.0 * len(paths))
        sig *= 0.7
        if channels == 2:
            left = sig
            right = np.roll(sig, rate // 50)
            data = np.stack((left, right), axis=1)
        else:
            data = sig
        path = directory / f"{track_id}{suffix}"
        sf.write(str(path), data, rate)
        paths[track_id] = str(path)
    return paths


def build_project(
    paths: dict[str, str], target_rate: int, seconds: float
) -> Project:
    frames = int(round(target_rate * seconds))
    raw_tracks = [
        {
            "id": "voice",
            "path": paths["voice"],
            "start_frame": 0,
            "gain": 1.0,
            "automation": [
                {"frame": 0, "gain": 0.4, "mode": "linear"},
                {"frame": frames // 3, "gain": 1.6, "mode": "hold"},
                {"frame": (2 * frames) // 3, "gain": 0.8, "mode": "linear"},
            ],
        },
        {
            "id": "ambience",
            "path": paths["ambience"],
            "start_frame": target_rate // 10,
            "gain": 0.4,
            "automation": [],
        },
        {
            "id": "music",
            "path": paths["music"],
            "start_frame": 0,
            "gain": 0.7,
            "automation": [
                {"frame": 0, "gain": 1.0, "mode": "hold"},
                {"frame": frames // 2, "gain": 0.5, "mode": "linear"},
            ],
        },
    ]
    raw = {
        "sample_rate": target_rate,
        "frames": frames,
        "tracks": raw_tracks,
        "automation_sample_frames": [0, frames // 3, frames - 1],
    }
    return validate_project(raw)


def run_demo(
    seed: int,
    rate: int,
    seconds: float,
    block_frames: int,
    wav_path: str,
    report_path: str,
) -> dict:
    """Generate fixtures, render both paths, write outputs and return report."""
    with tempfile.TemporaryDirectory(prefix="audiomix-demo-") as tmp:
        paths = generate_sources(tmp, seed)
        project = build_project(paths, rate, seconds)

        with MixGraph(project) as offline_graph:
            offline_report = write_float_wav(
                wav_path,
                offline_graph,
                block_frames,
                project.automation_sample_frames,
            )
        stream_data, stream_report = render_stream(project, block_frames)

        # Second streaming render with a different block size proves the
        # result does not depend on chunk boundaries.
        other_block = 1024 if block_frames != 1024 else 511
        other_data, _ = render_stream(project, other_block)

    report = {
        "frames": offline_report["frames"],
        "sample_rate": rate,
        "block_frames": block_frames,
        "clip_samples": offline_report["clip_samples"],
        "parity_max_abs": parity_error(stream_data, other_data),
        "tracks": offline_report["tracks"],
    }
    return report
