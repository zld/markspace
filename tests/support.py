"""Shared test helpers for the audiosplit pipeline tests."""

from __future__ import annotations

import io
import shutil
import sys
import tempfile
import unittest
import wave
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

import numpy as np

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
sys.path.insert(0, str(ROOT / "src"))

import make_samples  # noqa: E402,F401
from audiosplit import cli  # noqa: E402

SR = 44100


def write_wav(path: Path, samples: np.ndarray, rate: int = SR, bits: int = 16) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    clipped = np.clip(np.asarray(samples, dtype=np.float64), -1.0, 1.0)
    if bits == 16:
        pcm = np.round(clipped * 32767.0).astype("<i2")
    elif bits == 8:
        pcm = np.round(clipped * 127.0 + 128.0).astype(np.uint8)
    else:
        pcm = clipped.astype("<f4")
    with wave.open(str(path), "wb") as handle:
        handle.setnchannels(1)
        handle.setsampwidth(bits // 8)
        handle.setframerate(rate)
        handle.writeframes(pcm.tobytes())


def run_cli(args: list[str]) -> tuple[int, str, str]:
    out, err = io.StringIO(), io.StringIO()
    with redirect_stdout(out), redirect_stderr(err):
        code = cli.main(args)
    return code, out.getvalue(), err.getvalue()


def base_args(tmp: Path) -> list[str]:
    return [
        "--db", str(tmp / "audio.sqlite"),
        "--frame-ms", "10",
        "--noise-percentile", "20",
        "--margin-db", "8",
        "--merge-ms", "35",
    ]


class Harness(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp(prefix="audiosplit-test-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
