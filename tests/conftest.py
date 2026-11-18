"""Shared fixture helpers for the test suite."""

from __future__ import annotations

import json
from pathlib import Path

import numpy as np
import pytest
import soundfile as sf


def write_wav(path: Path, samples: np.ndarray, rate: int,
              subtype: str = "FLOAT") -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    data = np.asarray(samples, dtype=np.float64)
    if data.ndim == 1:
        data = data[:, None]
    sf.write(str(path), data, rate, format="WAV", subtype=subtype)
    return path


def tone(frames: int, rate: int, freq: float = 440.0, channels: int = 1,
         level: float = 0.25) -> np.ndarray:
    t = np.arange(frames, dtype=np.float64) / rate
    wave = np.sin(2 * np.pi * freq * t) * level
    if channels == 1:
        return wave
    return np.stack([wave] * channels, axis=1)


def write_manifest(path: Path, tracks: list[dict], **extra) -> Path:
    payload = {"tracks": tracks, **extra}
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload), encoding="utf-8")
    return path


@pytest.fixture
def audio_dir(tmp_path: Path) -> Path:
    return tmp_path / "audio"
