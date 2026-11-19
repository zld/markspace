"""Shared pytest fixtures: temp sessions built from generated WAVs."""

from __future__ import annotations

import json
from pathlib import Path

import numpy as np
import pytest
import soundfile as sf

from audiomix.model import Session, parse_manifest


@pytest.fixture
def session_dir(tmp_path: Path) -> Path:
    return tmp_path


def write_wav(path: Path, data: np.ndarray, rate: int) -> Path:
    """Write a float WAV in the layout the tests expect (2-D frames x chans)."""
    path.parent.mkdir(parents=True, exist_ok=True)
    sf.write(str(path), data.astype(np.float64), rate, subtype="FLOAT")
    return path


def build_session(directory: Path, manifest: dict) -> Session:
    path = directory / "session.json"
    path.write_text(json.dumps(manifest), encoding="utf-8")
    return parse_manifest(json.loads(path.read_text(encoding="utf-8")), path)


def ramp(frames: int, channels: int = 1, scale: float = 1.0) -> np.ndarray:
    """Deterministic ramp material: distinct per channel and per frame."""
    base = np.linspace(-scale, scale, frames, dtype=np.float64)
    return np.repeat(base[:, None], channels, axis=1)


@pytest.fixture
def mono_ramp(session_dir: Path) -> Path:
    return write_wav(session_dir / "mono.wav", ramp(800), 48000)
