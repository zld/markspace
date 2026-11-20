"""Generate the demo/test fixture: three WAV files plus a manifest.

Tracks (all content synthesized, no external assets):

- ``ambient_48k.wav``  — stereo 48 kHz low-level smoothed noise, full length
- ``music_44k1.wav``   — stereo 44.1 kHz sine sweep, starts at 0.25 s
- ``impact_32k.wav``   — mono 32 kHz decaying impulses peaking at 0.95,
  starts at 1.0 s with a +4 dB gain keyframe segment so the mix clips

Usage::

    PYTHONPATH=src python tools/make_fixture.py --out .tmp/fixture --duration 3
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path

import numpy as np
import soundfile as sf


def _ambient(duration: float, rate: int, rng: np.random.Generator) -> np.ndarray:
    n = int(round(duration * rate))
    noise = rng.standard_normal(n)
    # Cheap one-pole smoothing for a rumble-like bed.
    kernel = np.ones(64) / 64.0
    smooth = np.convolve(noise, kernel, mode="same")
    t = np.arange(n) / rate
    lfo = 0.7 + 0.3 * np.sin(2.0 * np.pi * 0.2 * t)
    left = smooth * lfo * 0.05
    right = np.roll(smooth, 137) * lfo * 0.05
    return np.column_stack([left, right])


def _music(duration: float, rate: int) -> np.ndarray:
    n = int(round(duration * rate))
    t = np.arange(n) / rate
    freq = 220.0 * np.power(2.0, t / max(duration, 1e-9))  # 220 -> 440 Hz sweep
    phase = 2.0 * np.pi * np.cumsum(freq) / rate
    tone = np.sin(phase) * 0.2
    return np.column_stack([tone, tone * 0.8])


def _impact(duration: float, rate: int) -> np.ndarray:
    n = int(round(duration * rate))
    t = np.arange(n) / rate
    out = np.zeros(n)
    for hit in np.arange(0.0, duration, 0.4):
        env = np.exp(-30.0 * np.maximum(t - hit, 0.0)) * (t >= hit)
        out += 0.95 * env * np.sin(2.0 * np.pi * 60.0 * t)
    return out


def build_fixture(out_dir: Path, duration: float) -> Path:
    out_dir.mkdir(parents=True, exist_ok=True)
    rng = np.random.default_rng(20240101)

    sf.write(str(out_dir / "ambient_48k.wav"), _ambient(duration, 48000, rng),
             48000, format="WAV", subtype="FLOAT")
    sf.write(str(out_dir / "music_44k1.wav"), _music(duration, 44100),
             44100, format="WAV", subtype="FLOAT")
    sf.write(str(out_dir / "impact_32k.wav"), _impact(duration, 32000),
             32000, format="WAV", subtype="FLOAT")

    manifest = {
        "tracks": [
            {"id": "ambient", "path": "ambient_48k.wav", "start": 0.0},
            {"id": "music", "path": "music_44k1.wav", "start": 0.25},
            {
                "id": "impact",
                "path": "impact_32k.wav",
                "start": 1.0,
                "gain": [
                    {"time": 0.0, "db": 4.0},
                    {"time": 1.0, "db": 4.0},
                    {"time": 1.5, "db": -2.0},
                ],
            },
        ]
    }
    manifest_path = out_dir / "manifest.json"
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    return manifest_path


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Generate mixforge demo fixture.")
    parser.add_argument("--out", required=True, help="output directory")
    parser.add_argument("--duration", type=float, default=3.0,
                        help="fixture duration in seconds")
    args = parser.parse_args(argv)
    manifest_path = build_fixture(Path(args.out), args.duration)
    print(f"fixture written to {args.out}")
    print(f"manifest: {manifest_path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
