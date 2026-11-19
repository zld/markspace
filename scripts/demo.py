"""Generate a deterministic sample project under ``--workdir``.

Usage::

    .venv/bin/python scripts/demo.py --workdir .demo

The fixture is 48 kHz, 5 s, three tracks from different source rates, with
linear/step automation, fades, a music bus, a loop region and a limiter.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

import numpy as np

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "src"))

from mixforge.io import write_json, write_wav  # noqa: E402

TARGET_RATE = 48_000
DURATION_SECONDS = 5.0


def dialogue(frames: int) -> np.ndarray:
    """Mono 'dialogue' burst: two tone chunks with silence between."""

    t = np.arange(frames) / TARGET_RATE
    signal = 0.55 * np.sin(2.0 * np.pi * 220.0 * t)
    signal += 0.20 * np.sin(2.0 * np.pi * 440.0 * t + 0.3)
    gate = ((t > 0.05) & (t < 1.2)) | ((t > 2.1) & (t < 2.75))
    signal = signal * gate
    return signal[:frames]


def music(frames: int) -> np.ndarray:
    """Stereo music bed: a chord-like stack with an excursion over 1.0."""

    t = np.arange(frames) / TARGET_RATE
    left = 0.62 * np.sin(2.0 * np.pi * 110.0 * t)
    left += 0.28 * np.sin(2.0 * np.pi * 165.0 * t + 1.1)
    right = 0.58 * np.sin(2.0 * np.pi * 110.0 * t + 0.4)
    right += 0.30 * np.sin(2.0 * np.pi * 220.0 * t + 0.2)
    swell = 1.0 + 0.9 * np.exp(-0.5 * ((t - 4.1) / 0.35) ** 2)
    return np.column_stack([left * swell, right * swell])


def ambience(frames: int) -> np.ndarray:
    """Mono deterministic noise bed (fixed seed)."""

    rng = np.random.default_rng(20240517)
    noise = rng.standard_normal(frames) * 0.08
    # Slow deterministic low-pass so it reads as ambience, not hiss.
    blurred = np.convolve(noise, np.ones(24) / 24.0, mode="same")
    return blurred


def seconds(value: float) -> float:
    return float(value)


def build_workdir(workdir: Path) -> dict[str, Path]:
    assets = workdir / "assets"
    assets.mkdir(parents=True, exist_ok=True)

    # 44.1 kHz stereo music, 4.5 s.
    music_rate = 44_100
    music_frames = int(round(4.5 * music_rate))
    music_data = music(music_frames)
    music_path = assets / "music_bed.wav"
    write_wav(music_path, music_data, music_rate)

    # 48 kHz mono dialogue, 3.0 s.
    dialogue_rate = 48_000
    dialogue_frames = int(round(3.0 * dialogue_rate))
    dialogue_path = assets / "dialogue.wav"
    write_wav(dialogue_path, dialogue(dialogue_frames)[:, None], dialogue_rate)

    # 32 kHz mono ambience, 5.0 s.
    ambience_rate = 32_000
    ambience_frames = int(round(5.0 * ambience_rate))
    ambience_path = assets / "ambience.wav"
    write_wav(ambience_path, ambience(ambience_frames)[:, None], ambience_rate)

    project = {
        "sample_rate": TARGET_RATE,
        "channels": 2,
        "duration_seconds": DURATION_SECONDS,
        "tracks": [
            {
                "id": "dialogue",
                "path": "assets/dialogue.wav",
                "start_seconds": seconds(0.25),
                "gain_db": -1.5,
                "automation": {
                    "unit": "seconds",
                    "interpolation": "linear",
                    "points": [
                        {"time": 0.0, "gain_db": -6.0},
                        {"time": 0.25, "gain_db": 0.0},
                        {"time": 1.0, "gain_db": -3.0},
                        {"time": 1.75, "gain_db": -1.0},
                        {"time": 2.5, "gain_db": -8.0},
                        {"time": 3.25, "gain_db": -12.0},
                    ],
                },
                "fade": {"in_seconds": 0.02, "out_seconds": 0.05},
            },
            {
                "id": "music",
                "path": "assets/music_bed.wav",
                "start_frame": 0,
                "gain_db": -2.0,
                "bus": "music",
                "automation": {
                    "unit": "frames",
                    "interpolation": "step",
                    "points": [
                        {"time": 0, "gain_db": -24.0},
                        {"time": 24_000, "gain_db": -12.0},
                        {"time": 120_000, "gain_db": -6.0},
                    ],
                },
            },
            {
                "id": "ambience",
                "path": "assets/ambience.wav",
                "start_seconds": seconds(0.1),
                "gain_db": -14.0,
                "automation": {
                    "unit": "seconds",
                    "interpolation": "linear",
                    "points": [
                        {"time": 0.0, "gain_db": 0.0},
                        {"time": 2.0, "gain_db": 6.0},
                        {"time": 4.0, "gain_db": -3.0},
                    ],
                },
            },
        ],
        "buses": [{"id": "music", "gain_db": -3.0}],
        "master": {"gain_db": -0.5, "limiter": {"enabled": True, "ceiling": 0.98}},
        "loop": {"start_seconds": 1.0, "end_seconds": 1.5, "count": 3},
    }
    project_path = workdir / "project.json"
    write_json(project_path, project)
    return {"project": project_path, "assets": assets}


def main() -> int:
    parser = argparse.ArgumentParser(description="Generate the MixForge demo project")
    parser.add_argument("--workdir", type=Path, default=Path(".demo"))
    args = parser.parse_args()
    outputs = build_workdir(args.workdir)
    print(
        json.dumps(
            {
                "status": "ok",
                "workdir": str(args.workdir),
                "project": str(outputs["project"]),
                "assets": sorted(str(p) for p in outputs["assets"].glob("*.wav")),
            },
            indent=2,
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
