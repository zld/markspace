"""Shared fixtures for the unittest suite (imports from tests/ only)."""

from __future__ import annotations

import os
import shutil
import sys
import tempfile

import numpy as np

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPTS = os.path.join(REPO_ROOT, "scripts")
if REPO_ROOT not in sys.path:
    sys.path.insert(0, REPO_ROOT)

from scripts import make_fixtures  # noqa: E402  (after sys.path fix)


class TempDirMixin:
    """Create a fresh temporary directory per test."""

    def setUp(self) -> None:  # noqa: D401 - unittest hook
        self.tmpdir = tempfile.mkdtemp(prefix="audiopipe-test-")
        self.addCleanup(shutil.rmtree, self.tmpdir, ignore_errors=True)

    def path(self, *parts: str) -> str:
        return os.path.join(self.tmpdir, *parts)


def write_wav(path: str, data: np.ndarray, sample_rate: int, subtype: str = "PCM_16") -> None:
    """Small deterministic WAV helper used across tests."""
    import soundfile as sf

    os.makedirs(os.path.dirname(os.path.abspath(path)) or ".", exist_ok=True)
    sf.write(path, data, sample_rate, subtype=subtype, format="WAV")


def sine(freq: float, sample_rate: int, frames: int, amplitude: float = 0.5) -> np.ndarray:
    t = np.arange(frames) / sample_rate
    return amplitude * np.sin(2.0 * np.pi * freq * t)


def demo_fixtures(root: str) -> list[str]:
    """Write the deterministic demo set into ``root``."""
    os.makedirs(root, exist_ok=True)
    return make_fixtures.write_demo_set(root)


def edge_fixtures(root: str) -> list[str]:
    os.makedirs(root, exist_ok=True)
    return make_fixtures.write_edge_set(root)
