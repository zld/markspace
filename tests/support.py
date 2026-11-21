"""Shared test helpers."""

from __future__ import annotations

import shutil
import tempfile
import unittest
from pathlib import Path

import numpy as np
import soundfile as sf


def write_audio(path: Path, samples: np.ndarray, rate: int, **kwargs) -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    sf.write(str(path), samples, rate, **kwargs)
    return path


class TempDirTest(unittest.TestCase):
    """Each test gets an isolated directory that is removed afterwards."""

    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp(prefix="audiorepro-test-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
