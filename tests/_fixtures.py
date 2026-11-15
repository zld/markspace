"""Shared deterministic fixtures for the audiomix test suite."""

from __future__ import annotations

import tempfile
from pathlib import Path

import numpy as np
import soundfile as sf

from audiomix.model import validate_project


def write_audio(path: Path, rate: int, channels: int, frames: int, seed=0):
    rng = np.random.default_rng(seed)
    data = rng.standard_normal((frames, channels) if channels > 1 else frames)
    data *= 0.2
    sf.write(str(path), data, rate)
    return data


class ProjectFixture:
    """A small three-track project (44100 mono, 48000 stereo, 32000 mono)."""

    def __init__(self, tmpdir: str, frames: int = 4800, rate: int = 48000):
        self.tmp = Path(tmpdir)
        self.rate = rate
        self.frames = frames
        write_audio(self.tmp / "mono44.flac", 44100, 1, 4410, seed=1)
        write_audio(self.tmp / "stereo48.wav", 48000, 2, frames, seed=2)
        write_audio(self.tmp / "mono32.flac", 32000, 1, 4000, seed=3)
        self.paths = {
            "a": str(self.tmp / "mono44.flac"),
            "b": str(self.tmp / "stereo48.wav"),
            "c": str(self.tmp / "mono32.flac"),
        }

    def raw(self):
        return {
            "sample_rate": self.rate,
            "frames": self.frames,
            "tracks": [
                {"id": "a", "path": self.paths["a"], "start_frame": 0, "gain": 1.0},
                {"id": "b", "path": self.paths["b"], "start_frame": 100, "gain": 0.5},
                {"id": "c", "path": self.paths["c"], "start_frame": 0, "gain": 1.0},
            ],
            "automation_sample_frames": [0, self.frames // 2, self.frames - 1],
        }

    def project(self):
        return validate_project(self.raw())


class TempTree:
    """Context manager creating and cleaning a temporary fixture directory."""

    def __init__(self):
        self._dir = tempfile.TemporaryDirectory(prefix="audiomix-test-")
        self.path = self._dir.name

    def __enter__(self) -> ProjectFixture:
        return ProjectFixture(self.path)

    def __exit__(self, *exc) -> None:
        self._dir.cleanup()
