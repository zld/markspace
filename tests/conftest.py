"""Shared pytest fixtures: a reproducible sample sequence and CLI runner."""

from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "scripts"))

from cutline.model import Clip, RateSegment, Sequence, Track  # noqa: E402
from cutline.timebase import FrameRate  # noqa: E402
from fractions import Fraction  # noqa: E402

PROJECT_ROOT = Path(__file__).resolve().parents[1]


@pytest.fixture
def sample_sequence() -> Sequence:
    rate = FrameRate(30000, 1001)
    video = Track(
        id="v1",
        kind="video",
        level=1,
        clips=(
            Clip(id="a", media_id="m/a.mov", timeline_start=0, duration=48,
                 source_in=0, source_duration=96, fade_in=8),
            Clip(id="b", media_id="m/b.mov", timeline_start=36, duration=36,
                 source_in=12, source_duration=96, priority=4),
            Clip(id="c", media_id="m/c.mov", timeline_start=60, duration=24,
                 source_in=0, source_duration=60, priority=1, fade_out=6),
        ),
    )
    audio = Track(
        id="a1",
        kind="audio",
        level=0,
        clips=(
            Clip(id="vo", media_id="m/vo.wav", timeline_start=0, duration=60,
                 source_in=0, source_duration=60),
        ),
    )
    return Sequence(id="fixture", frame_rate=rate, tracks=(audio, video), name="fixture")


@pytest.fixture
def fixture_path(tmp_path: Path, sample_sequence: Sequence) -> Path:
    from cutline.exchange import write

    path = tmp_path / "fixture.json"
    write(sample_sequence, path, version=2)
    return path


def run_cli(*args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, "-m", "cutline.cli", *args],
        capture_output=True,
        text=True,
        cwd=PROJECT_ROOT,
        check=False,
    )


def cli_json(*args: str) -> dict:
    result = run_cli(*args)
    assert result.returncode == 0, result.stderr
    return json.loads(result.stdout)
