#!/usr/bin/env python3
"""Generate a deterministic mixforge fixture under ``--out``.

The fixture exercises mixed sample rates, automation kinks, hold
interpolation, mute segments, short/empty/single-sample sources, an
over-level track and polarity cancellation.  No external assets are used.
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path

import numpy as np
import soundfile as sf


def sine(freq: float, frames: int, rate: int, amp: float,
         phase: float = 0.0) -> np.ndarray:
    t = np.arange(frames, dtype=np.float64) / rate
    return amp * np.sin(2.0 * np.pi * freq * t + phase)


def write_wav(path: Path, data: np.ndarray, rate: int) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    sf.write(str(path), data, rate, subtype="FLOAT", format="WAV")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", required=True)
    args = parser.parse_args()
    root = Path(args.out)
    root.mkdir(parents=True, exist_ok=True)

    # 1. Mono dialogue, 48k, one second; automation rides its level with a
    #    linear kink at 0.25s and a hold segment thereafter.
    dialogue = sine(220.0, 48_000, 48_000, 0.2)
    write_wav(root / "dialogue.wav", dialogue.reshape(-1, 1), 48_000)

    # 2. Stereo ambience, 44.1k, ~1.1s, truncated by the project tail.
    frames_amb = int(44_100 * 1.1)
    t = np.arange(frames_amb, dtype=np.float64) / 44_100
    amb = np.column_stack([
        0.3 * np.sin(2 * np.pi * 110 * t),
        0.3 * np.sin(2 * np.pi * 137.5 * t + 0.4),
    ])
    write_wav(root / "ambience.wav", amb, 44_100)

    # 3. Over-level cue at 96k: intentional clipping source (>1.0 linear).
    cue = sine(880.0, 9_600, 96_000, 2.0)
    write_wav(root / "cue.wav", cue.reshape(-1, 1), 96_000)

    # 4. Polarity pair (48k mono): equal and opposite, cancel on the bus.
    positive = sine(330.0, 24_000, 48_000, 0.25).reshape(-1, 1)
    write_wav(root / "polar_pos.wav", positive, 48_000)
    write_wav(root / "polar_neg.wav", -positive, 48_000)

    # 5. Edge cases: one-sample file and an empty (zero-frame) file.
    write_wav(root / "one_sample.wav", np.array([[0.5]], dtype=np.float64), 48_000)
    write_wav(root / "empty.wav", np.zeros((0, 1), dtype=np.float64), 48_000)

    project = {
        "project_rate": 48_000,
        "frames": 48_000,
        "channels": 2,
        "clip_policy": "preserve",
        "clip_threshold": 1.0,
        "automation_interval": 0.1,
        "output": "mix.wav",
        "tracks": [
            {
                "name": "dialogue",
                "file": "dialogue.wav",
                "start_frame": 0,
                "gain_db": 0.0,
                "layout": {"mode": "mono_copy"},
                "automation": [
                    {"time": 0.0, "gain_db": -6.0, "interpolation": "linear"},
                    {"time": 0.25, "gain_db": 0.0, "interpolation": "linear"},
                    {"time": 0.5, "gain_db": -3.0, "interpolation": "hold"},
                ],
                "mutes": [{"start": 0.7, "end": 0.8}],
            },
            {
                "name": "ambience",
                "file": "ambience.wav",
                "start_frame": 0,
                "gain_db": -2.0,
                "layout": {"mode": "stereo"},
                "automation": [],
            },
            {
                "name": "cue",
                "file": "cue.wav",
                "start_frame": 24_000,
                "gain_db": 0.0,
                "layout": {"mode": "mono_copy"},
                # Hold point beyond the project end: the keyframe value is
                # never reached, so the deliberately hot level is retained.
                "automation": [
                    {"time": 1.5, "gain_db": -12.0, "interpolation": "hold"},
                ],
            },
            {
                "name": "polar_pos",
                "file": "polar_pos.wav",
                "start_frame": 0,
                "gain_db": 0.0,
                "layout": {"mode": "mono_copy"},
            },
            {
                "name": "polar_neg",
                "file": "polar_neg.wav",
                "start_frame": 0,
                "gain_db": 0.0,
                "layout": {"mode": "mono_copy"},
            },
            {
                "name": "one_sample",
                "file": "one_sample.wav",
                "start_frame": 40_000,
                "gain_db": 0.0,
                "layout": {"mode": "mono_copy"},
            },
            {
                "name": "empty",
                "file": "empty.wav",
                "start_frame": 0,
                "gain_db": 0.0,
                "layout": {"mode": "mono_copy"},
            },
        ],
    }
    (root / "project.json").write_text(
        json.dumps(project, indent=2, sort_keys=True), encoding="utf-8")
    print(f"fixture written to {root}")


if __name__ == "__main__":
    main()
