#!/usr/bin/env python3
"""Generate deterministic WAV fixtures and a mix-session manifest.

Everything is synthesized with NumPy (no external assets): sine tones,
phase-shifted tones and seeded noise at two sample rates, including a
mono track and a deliberately short tail track.
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path

import numpy as np
import soundfile as sf

PROJECT_RATE = 48000
ALT_RATE = 24000


def _time_axis(frames: int, rate: int) -> np.ndarray:
    return np.arange(frames, dtype=np.float64) / rate


def sine_wave(frequency: float, frames: int, rate: int, amplitude: float,
              phase: float = 0.0) -> np.ndarray:
    times = _time_axis(frames, rate)
    return amplitude * np.sin(2.0 * np.pi * frequency * times + phase)


def seeded_noise(frames: int, seed: int, amplitude: float) -> np.ndarray:
    rng = np.random.default_rng(seed)
    return amplitude * rng.standard_normal(frames)


def write_wav(path: Path, data: np.ndarray, rate: int) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    sf.write(str(path), data, rate, format="WAV", subtype="FLOAT")


def build_fixtures(out_dir: Path) -> dict:
    out_dir.mkdir(parents=True, exist_ok=True)

    # Mono dialog at half the project rate: resampling + mono->stereo map.
    dialog_frames = int(round(1.5 * ALT_RATE))
    dialog = sine_wave(330.0, dialog_frames, ALT_RATE, 0.8)[..., np.newaxis]
    write_wav(out_dir / "dialog.wav", dialog, ALT_RATE)

    # Stereo ambience spanning the full ~2 s timeline.
    amb_frames = int(round(2.0 * PROJECT_RATE))
    left = sine_wave(110.0, amb_frames, PROJECT_RATE, 0.5)
    right = sine_wave(110.0, amb_frames, PROJECT_RATE, 0.5, phase=np.pi / 2)
    ambience = np.stack([left, right], axis=1)
    write_wav(out_dir / "ambience.wav", ambience, PROJECT_RATE)

    # Stereo music with automation whose node lands on a block boundary at
    # block size 257: 257 * ~187.55 -> node at t = 1.0 s (frame 48000).
    music_frames = int(round(2.0 * PROJECT_RATE))
    music_l = sine_wave(220.0, music_frames, PROJECT_RATE, 0.6)
    music_r = sine_wave(220.0, music_frames, PROJECT_RATE, 0.6, phase=np.pi)
    music = np.stack([music_l, music_r], axis=1)
    write_wav(out_dir / "music.wav", music, PROJECT_RATE)

    # Short stereo effect: tail shorter than one 257-frame block remains.
    sfx_frames = int(round(1.25 * PROJECT_RATE))
    sfx_data = seeded_noise(sfx_frames, seed=20261120, amplitude=0.3)
    sfx_stereo = np.stack([sfx_data, -sfx_data], axis=1)
    write_wav(out_dir / "sfx.wav", sfx_stereo, PROJECT_RATE)

    manifest = {
        "name": "fixture-session",
        "sample_rate": PROJECT_RATE,
        "channels": 2,
        "block_size": 257,
        "limiter": False,
        "tracks": [
            {
                "name": "dialog",
                "path": "dialog.wav",
                "offset": 0.2,
                "gain": 1.0,
                "channel_map": [[0, 0], [0, 1]],
                "automation": {
                    "interpolation": "linear",
                    "nodes": [
                        {"time": 0.0, "gain": 0.0},
                        {"time": 0.2, "gain": 1.0},
                        {"time": 1.4, "gain": 0.6},
                    ],
                },
            },
            {
                "name": "ambience",
                "path": "ambience.wav",
                "offset": 0.0,
                "gain": 0.8,
            },
            {
                "name": "music",
                "path": "music.wav",
                "offset": 0.0,
                "gain": 1.0,
                "automation": {
                    "interpolation": "hold",
                    "nodes": [
                        {"time": 0.0, "gain": 1.0},
                        {"time": 1.0, "gain": 0.4},
                        {"time": 1.5, "gain": 0.9},
                    ],
                },
            },
            {
                "name": "sfx",
                "path": "sfx.wav",
                "offset": 0.5,
                "gain": 0.5,
            },
        ],
    }
    (out_dir / "session.json").write_text(
        json.dumps(manifest, indent=2), encoding="utf-8"
    )
    return manifest


def main() -> int:
    parser = argparse.ArgumentParser(description="Generate mixforge fixtures")
    parser.add_argument("--out", required=True, help="output directory")
    args = parser.parse_args()
    manifest = build_fixtures(Path(args.out))
    print(f"wrote {len(manifest['tracks'])} tracks and session.json to {args.out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
