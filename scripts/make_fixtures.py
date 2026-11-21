#!/usr/bin/env python3
"""Generate sample WAV assets and a scene manifest for audiomix.

Creates a dense battlefield-style encounter: short one-shots, a looping
mechanical bed and a regional alarm, at mixed sample rates and channel
layouts.  Deterministic for a given --seed.
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path

import numpy as np
import soundfile as sf


def _sine_burst(rng, rate, dur, freq, decay, amp):
    t = np.arange(int(rate * dur)) / rate
    env = np.exp(-t * decay)
    return (amp * env * np.sin(2 * np.pi * freq * t)).astype(np.float64)


def _noise_burst(rng, rate, dur, decay, amp):
    n = int(rate * dur)
    t = np.arange(n) / rate
    return (amp * np.exp(-t * decay) * rng.standard_normal(n)).astype(np.float64)


def _mech_loop(rng, rate, dur):
    """Looping mechanical whir: fundamental + harmonics + AM, stereo."""
    t = np.arange(int(rate * dur)) / rate
    f0 = 110.0
    sig = sum(np.sin(2 * np.pi * f0 * k * t + 0.3 * k) / k for k in (1, 2, 3, 5))
    am = 0.5 + 0.5 * np.sin(2 * np.pi * 7.0 * t)
    rumble = 0.15 * rng.standard_normal(len(t))
    left = 0.45 * sig * am + rumble
    right = 0.45 * np.roll(sig, 17) * am + np.roll(rumble, 31)
    return np.column_stack([left, right])


def _siren(rng, rate, dur):
    t = np.arange(int(rate * dur)) / rate
    sweep = 600 + 400 * np.sin(2 * np.pi * 2.5 * t)
    phase = np.cumsum(sweep) / rate * 2 * np.pi
    return (0.5 * np.sin(phase)).astype(np.float64)


def _impacts(rng, rate, count, spacing):
    """Stacked loud transients, stereo, hot enough to clip when summed."""
    n = int(rate * spacing * (count + 1))
    out = np.zeros((n, 2))
    for i in range(count):
        start = int(i * spacing * rate + rng.integers(0, 200))
        burst = _sine_burst(rng, rate, 0.3, 60 + 20 * i, 18.0, 0.95)
        end = min(start + len(burst), n)
        out[start:end, 0] += burst[: end - start]
        out[start:end, 1] += 0.9 * burst[: end - start]
    return out


def _radio(rng, rate, dur):
    n = int(rate * dur)
    sig = 0.25 * rng.standard_normal(n)
    gate = (np.sin(2 * np.pi * 3.0 * np.arange(n) / rate) > 0).astype(float)
    return (sig * gate).astype(np.float64)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--dir", required=True, help="output directory for assets")
    ap.add_argument("--seed", type=int, default=0)
    args = ap.parse_args()

    rng = np.random.default_rng(args.seed)
    out = Path(args.dir)
    out.mkdir(parents=True, exist_ok=True)

    assets = {
        "mech_loop.wav": (_mech_loop(rng, 48000, 2.8), 48000),
        "kick.wav": (_sine_burst(rng, 44100, 0.25, 55.0, 14.0, 0.9), 44100),
        "snare.wav": (_noise_burst(rng, 32000, 0.18, 22.0, 0.7), 32000),
        "alarm.wav": (_siren(rng, 22050, 1.6), 22050),
        "impact.wav": (_impacts(rng, 48000, 4, 0.35), 48000),
        "radio.wav": (_radio(rng, 16000, 0.9), 16000),
    }
    for name, (data, rate) in assets.items():
        sf.write(out / name, data, rate, subtype="FLOAT")

    manifest = {
        "buses": {
            "ambience": {"target": "master"},
            "machines": {"target": "ambience"},
        },
        "tracks": [
            {"file": "mech_loop.wav", "offset": 0.0, "bus": "machines",
             "curve": [[0.0, -3.0], [1.0, 0.0], [2.5, -6.0]]},
            {"file": "kick.wav", "offset": 0.20, "bus": "master",
             "curve": [[0.0, 0.0], [1.0, -2.0], [2.0, 0.0]]},
            {"file": "snare.wav", "offset": 0.45, "bus": "master",
             "curve": [[0.0, -1.5], [1.5, -4.0], [2.8, -1.0]]},
            {"file": "alarm.wav", "offset": 1.00, "bus": "ambience",
             "curve": [[0.5, -12.0], [1.0, 0.0], [2.0, -3.0]]},
            {"file": "impact.wav", "offset": 1.30, "bus": "master",
             "curve": [[1.3, 3.0], [2.0, 1.0], [2.9, -2.0]]},
            {"file": "radio.wav", "offset": 2.00, "bus": "machines",
             "curve": [[2.0, -6.0], [2.4, 0.0], [2.9, -9.0]]},
        ],
    }
    scene = out / "scene.json"
    scene.write_text(json.dumps(manifest, indent=2), encoding="utf-8")
    print(f"wrote {len(assets)} WAV assets and {scene}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
