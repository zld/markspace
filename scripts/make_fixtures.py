"""Generate reproducible example WAV sources and a session manifest.

Run::

    python scripts/make_fixtures.py --out .venv/fixtures

All randomness uses a fixed NumPy generator seed, so repeated runs produce
byte-identical WAV files.  Sources deliberately differ in sample rate,
channel count, start time and length to exercise the whole pipeline.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

import numpy as np
import soundfile as sf

SEED = 13217
OUTPUT_RATE = 48000


def _tone(freq: float, frames: int, rate: int, amp: float,
          rng: np.random.Generator) -> np.ndarray:
    t = np.arange(frames, dtype=np.float64) / rate
    sig = amp * np.sin(2.0 * np.pi * freq * t)
    sig += amp * 0.05 * rng.standard_normal(frames)
    # Fade in/out to avoid boundary clicks.
    fade = min(frames // 8, rate // 20)
    if fade > 0:
        env = np.ones(frames, dtype=np.float64)
        env[:fade] = np.linspace(0.0, 1.0, fade)
        env[-fade:] = np.linspace(1.0, 0.0, fade)
        sig *= env
    return sig


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", required=True, type=Path)
    args = parser.parse_args(argv)
    out: Path = args.out
    out.mkdir(parents=True, exist_ok=True)
    rng = np.random.default_rng(SEED)

    # 1) Voice: mono 44.1 kHz, starts at zero.
    voice = _tone(220.0, int(44100 * 2.0), 44100, 0.6, rng)
    sf.write(str(out / "voice.wav"), voice[:, np.newaxis], 44100,
             subtype="FLOAT")

    # 2) Music: stereo 48 kHz, starts at 0.25 s.
    n = int(48000 * 2.5)
    left = _tone(330.0, n, 48000, 0.35, rng)
    right = _tone(392.0, n, 48000, 0.30, rng)
    sf.write(str(out / "music.wav"), np.stack((left, right), axis=1),
             48000, subtype="FLOAT")

    # 3) Ambience: stereo 32 kHz with linear automation, starts at 0.5 s.
    n32 = int(32000 * 1.5)
    amb_l = _tone(110.0, n32, 32000, 0.25, rng)
    amb_r = _tone(116.5, n32, 32000, 0.25, rng)
    sf.write(str(out / "ambience.wav"),
             np.stack((amb_l, amb_r), axis=1), 32000, subtype="FLOAT")

    # 4) Blip: short mono 48 kHz source starting mid-timeline.
    blip = _tone(880.0, 4800, 48000, 0.8, rng)
    sf.write(str(out / "blip.wav"), blip[:, np.newaxis], 48000,
             subtype="FLOAT")

    manifest = {
        "output_rate": OUTPUT_RATE,
        "channels": 2,
        "bus_gain": 0.9,
        "tracks": [
            {
                "name": "voice",
                "file": "voice.wav",
                "start": 0.0,
                "gain": 1.0,
                "channel_map": [0, 0],
            },
            {
                "name": "music",
                "file": "music.wav",
                "start": 0.25,
                "gain": 0.8,
            },
            {
                "name": "ambience",
                "file": "ambience.wav",
                "start": 0.5,
                "gain": 1.0,
                "automation": {
                    "mode": "linear",
                    "points": [
                        {"time": 0.0, "value": 0.2},
                        {"time": 1.0, "value": 1.0},
                        {"time": 2.0, "value": 0.4},
                        {"time": 3.0, "value": 0.4},
                    ],
                },
            },
            {
                "name": "blip",
                "file": "blip.wav",
                "start": 1.75,
                "gain": 0.5,
                "muted": False,
                "automation": {
                    "mode": "db",
                    "points": [
                        {"time": 1.5, "value": None},
                        {"time": 1.75, "value": -6.0},
                        {"time": 2.5, "value": 0.0},
                    ],
                },
            },
        ],
    }
    (out / "session.json").write_text(
        json.dumps(manifest, indent=2, sort_keys=True), encoding="utf-8")
    print(f"fixtures written to {out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
