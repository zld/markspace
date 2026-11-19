"""Shared pytest fixtures and frame-record builders."""

from __future__ import annotations

import json
import subprocess
import sys
from fractions import Fraction
from pathlib import Path

import pytest

from frameaudit.model import FrameRecord

PROJECT_ROOT = Path(__file__).resolve().parent.parent
SCRIPT = PROJECT_ROOT / "scripts" / "make_fixtures.py"


def make_record(
    decode_index: int,
    pts: int | None,
    *,
    frame_type: str = "P",
    time_base: Fraction | None = None,
    key_frame: bool = False,
) -> FrameRecord:
    return FrameRecord(
        decode_index=decode_index,
        frame_type=frame_type,
        pts=pts,
        time_base=time_base or Fraction(1, 1000),
        key_frame=key_frame,
    )


@pytest.fixture(scope="session")
def fixture_video(tmp_path_factory) -> Path:
    """Generate the synthetic B-frame fixture once for the whole session."""
    target = tmp_path_factory.mktemp("media") / "bframes.mkv"
    manifest = target.with_suffix(target.suffix + ".manifest.json")
    subprocess.run(
        [sys.executable, str(SCRIPT), "--output", str(target)],
        check=True,
        capture_output=True,
        text=True,
    )
    assert target.exists() and target.stat().st_size > 0
    assert manifest.exists()
    return target


@pytest.fixture(scope="session")
def fixture_manifest(fixture_video) -> dict:
    manifest = fixture_video.with_suffix(
        fixture_video.suffix + ".manifest.json"
    )
    return json.loads(manifest.read_text(encoding="utf-8"))
