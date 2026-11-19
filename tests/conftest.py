"""Test fixtures: deterministic WAVs and tiny validated projects."""

from __future__ import annotations

from pathlib import Path

import numpy as np
import pytest

from mixforge.io import write_wav
from mixforge.model import Project, parse_project


def make_project(tmp_path: Path, spec: dict) -> Project:
    return parse_project(spec, source_dir=tmp_path, source="<test>")


@pytest.fixture()
def fixture_dir(tmp_path: Path) -> Path:
    assets = tmp_path / "assets"
    assets.mkdir()
    # 24 kHz mono ramp, 0.1 s: exercises resampling to 48 kHz.
    ramp = np.linspace(-0.8, 0.8, 2400, dtype=np.float64)[:, None]
    write_wav(assets / "ramp.wav", ramp, 24_000)
    # 48 kHz stereo steady tone, 0.2 s with a loud section that can clip.
    t = np.arange(9600) / 48_000.0
    tone = np.column_stack(
        [np.sin(2 * np.pi * 180 * t), 1.4 * np.sin(2 * np.pi * 240 * t)]
    )
    write_wav(assets / "tone.wav", tone, 48_000)
    # Silent mono clip.
    write_wav(assets / "silence.wav", np.zeros((4800, 1)), 48_000)
    return assets


def base_spec(**overrides: object) -> dict:
    spec = {
        "sample_rate": 48_000,
        "channels": 2,
        "duration_frames": 24_000,
        "tracks": [
            {
                "id": "ramp",
                "path": "assets/ramp.wav",
                "start_frame": 1000,
                "gain_db": -3.0,
                "automation": {
                    "unit": "frames",
                    "interpolation": "linear",
                    "points": [
                        {"time": 0, "gain_db": -12.0},
                        {"time": 5_000, "gain_db": 0.0},
                        {"time": 13_000, "gain_db": -6.0},
                    ],
                },
            },
            {
                "id": "tone",
                "path": "assets/tone.wav",
                "gain_db": 0.0,
                "bus": "music",
            },
            {
                "id": "silence",
                "path": "assets/silence.wav",
                "start_frame": 2_000,
                "mute": True,
            },
        ],
        "buses": [{"id": "music", "gain_db": -2.0}],
        "master": {"gain_db": -0.5, "limiter": {"enabled": True, "ceiling": 0.9}},
    }
    spec.update(overrides)
    return spec
