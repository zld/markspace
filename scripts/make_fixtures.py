#!/usr/bin/env python3
"""Generate the deterministic demo session and its source audio.

All audio is synthesised from seeded noise and fixed tones, so repeated runs
produce byte-identical fixtures. Tracks deliberately differ in sample rate,
channel layout and offset to exercise conversion and alignment.
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path

import numpy as np
import soundfile as sf

FIXTURE_VERSION = "1"


def _stereo_noise(rng: np.random.Generator, frames: int, level: float) -> np.ndarray:
    return rng.standard_normal((frames, 2)) * level


def _voiced(rng: np.random.Generator, frames: int, rate: int) -> np.ndarray:
    """A mono pseudo-voice: pitched tone with a slow amplitude envelope."""
    t = np.arange(frames, dtype=np.float64) / rate
    carrier = np.sin(2 * np.pi * 180.0 * t)
    modulation = 0.5 + 0.5 * np.sin(2 * np.pi * 1.7 * t)
    breath = rng.standard_normal(frames) * 0.01
    return (0.22 * carrier * modulation + breath)


def _music(rng: np.random.Generator, frames: int, rate: int) -> np.ndarray:
    """Stereo chord bed with a slow tremolo."""
    t = np.arange(frames, dtype=np.float64) / rate
    chord = (0.10 * np.sin(2 * np.pi * 220.0 * t)
             + 0.08 * np.sin(2 * np.pi * 277.18 * t)
             + 0.06 * np.sin(2 * np.pi * 329.63 * t))
    tremolo = 0.75 + 0.25 * np.sin(2 * np.pi * 0.5 * t)
    left = chord * tremolo + rng.standard_normal(frames) * 0.004
    right = chord * tremolo + rng.standard_normal(frames) * 0.004
    return np.stack([left, right], axis=1)


def _ambience(rng: np.random.Generator, frames: int, rate: int) -> np.ndarray:
    """Decorrelated stereo noise with a slow swell, plus a few transient hits."""
    left = rng.standard_normal(frames) * 0.05
    right = rng.standard_normal(frames) * 0.05
    t = np.arange(frames, dtype=np.float64) / rate
    swell = 0.55 + 0.45 * np.sin(2 * np.pi * 0.11 * t)
    left *= swell
    right *= swell
    for onset_s in (0.4, 1.4):
        start = int(onset_s * rate)
        length = min(int(0.08 * rate), frames - start)
        if length <= 0:
            continue
        hit = np.exp(-np.linspace(0, 12, length)) * 0.35
        left[start:start + length] += hit
        right[start:start + length] += hit
    return np.stack([left, right], axis=1)


def build_session() -> dict:
    return {
        "output_rate": 48000,
        "duration_ms": 2000,
        "buses": {
            "dialogue": {"gain_db": 0.0},
            "music": {"gain_db": -2.5,
                      "automation": [
                          {"ms": 0, "value": -9.0, "unit": "db"},
                          {"ms": 600, "value": -2.5, "unit": "db"},
                          {"ms": 1400, "value": -2.5, "unit": "db"},
                          {"ms": 2000, "value": -12.0, "unit": "db"},
                      ]},
            "ambience": {"gain_db": -4.0},
        },
        "tracks": [
            {
                "id": "dialogue",
                "path": "dialogue.wav",
                "offset_ms": 0,
                "channels": "mono",
                "gain_db": 3.0,
                "bus": "dialogue",
                "automation": [
                    {"ms": 0, "value": -18.0, "unit": "db"},
                    {"ms": 250, "value": 0.0},
                    {"ms": 900, "value": -6.0, "unit": "db"},
                    {"ms": 1600, "value": -2.0, "unit": "db"},
                    {"ms": 2000, "value": -30.0, "unit": "db"},
                ],
            },
            {
                "id": "music",
                "path": "music.wav",
                "offset_ms": 500,
                "channels": "stereo",
                "gain_db": 6.0,
                "bus": "music",
                "automation": [
                    {"ms": 500, "value": -12.0, "unit": "db"},
                    {"ms": 1000, "value": 0.0, "unit": "db"},
                    {"ms": 2000, "value": -3.0, "unit": "db"},
                ],
            },
            {
                "id": "ambience",
                "path": "ambience.wav",
                "offset_ms": 125,
                "channels": [[0.9, 0.1], [0.15, 1.0]],
                "gain_db": 2.0,
                "bus": "ambience",
                "automation": [
                    {"ms": 0, "value": 0.0},
                    {"ms": 750, "value": -8.0, "unit": "db"},
                    {"ms": 1500, "value": -4.0, "unit": "db"},
                    {"ms": 2000, "value": -14.0, "unit": "db"},
                ],
            },
        ],
    }


def generate(out_dir: Path) -> dict:
    """Write sources and manifest; return a summary of what was written."""
    out_dir.mkdir(parents=True, exist_ok=True)
    rng = np.random.default_rng(20261119)

    dialogue = _voiced(rng, int(1.6 * 44100), 44100)
    sf.write(out_dir / "dialogue.wav", dialogue, 44100,
             format="WAV", subtype="PCM_24")

    music = _music(rng, int(1.5 * 48000), 48000)
    sf.write(out_dir / "music.wav", music, 48000, format="WAV", subtype="FLOAT")

    ambience = _ambience(rng, int(1.5 * 22050), 22050)
    sf.write(out_dir / "ambience.wav", ambience, 22050,
             format="WAV", subtype="FLOAT")

    session = build_session()
    manifest_path = out_dir / "session.json"
    manifest_path.write_text(json.dumps(session, indent=2) + "\n",
                             encoding="utf-8")
    return {
        "manifest": str(manifest_path),
        "rate": session["output_rate"],
        "duration_ms": session["duration_ms"],
        "tracks": [track["id"] for track in session["tracks"]],
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", required=True, type=Path,
                        help="directory for generated audio and session.json")
    args = parser.parse_args(argv)
    summary = generate(args.out)
    print(f"wrote manifest={summary['manifest']}")
    print(f"tracks={len(summary['tracks'])} rate={summary['rate']} "
          f"duration_ms={summary['duration_ms']}")
    print("tracks: " + ", ".join(summary["tracks"]))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
