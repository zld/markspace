#!/usr/bin/env python3
"""Generate deterministic demo fixture tracks and a mix manifest.

All sample data is synthesised here — no external files are needed.
Produces three tracks at 44100 / 32000 / 48000 Hz (WAV, FLAC, WAV):
a dialogue-like mono track, a stereo ambience bed and stereo combat hits.

Usage: python scripts/generate_fixture.py [output_dir]
Prints the path of the generated manifest JSON.
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

import numpy as np
import soundfile as sf

OUTPUT_RATE = 48000


def _dialogue(rate=44100, seconds=2.5):
    """Mono speech-like signal: harmonic buzz with syllable envelopes."""
    t = np.arange(int(rate * seconds)) / rate
    pitch = 110.0 + 30.0 * np.sin(2 * np.pi * 1.7 * t)
    phase = np.cumsum(2 * np.pi * pitch / rate)
    voice = (np.sin(phase) + 0.5 * np.sin(2 * phase) + 0.25 * np.sin(3 * phase))
    syllables = 0.5 + 0.5 * np.sin(2 * np.pi * 3.1 * t + 0.4)
    envelope = np.clip(syllables, 0.05, 1.0) ** 1.5
    signal = voice * envelope
    return (0.8 * signal / np.max(np.abs(signal)))[:, None]


def _ambience(rate=32000, seconds=2.0):
    """Stereo noise bed, smoothed into a slowly swelling texture."""
    rng = np.random.default_rng(20240501)
    n = int(rate * seconds)
    noise = rng.standard_normal((n, 2))
    kernel = np.hanning(257)
    kernel /= kernel.sum()
    smooth = np.column_stack(
        [np.convolve(noise[:, c], kernel, mode="same") for c in range(2)])
    swell = (0.4 + 0.6 * np.linspace(0.0, 1.0, n))[:, None]
    signal = smooth * swell
    return 0.35 * signal / np.max(np.abs(signal))


def _combat(rate=48000, seconds=2.0):
    """Stereo percussive hits: decaying sine bursts with noise clicks."""
    n = int(rate * seconds)
    rng = np.random.default_rng(7)
    signal = np.zeros((n, 2))
    for hit_at, freq, pan in ((0.10, 90.0, -0.3), (0.60, 70.0, 0.25),
                              (1.10, 110.0, 0.0), (1.55, 80.0, 0.4)):
        start = int(hit_at * rate)
        length = min(n - start, int(0.35 * rate))
        t = np.arange(length) / rate
        burst = np.sin(2 * np.pi * freq * t) * np.exp(-t * 14.0)
        burst += 0.3 * rng.standard_normal(length) * np.exp(-t * 60.0)
        left = np.sqrt(0.5 * (1.0 - pan))
        right = np.sqrt(0.5 * (1.0 + pan))
        signal[start:start + length, 0] += burst * left
        signal[start:start + length, 1] += burst * right
    return 0.9 * signal / np.max(np.abs(signal))


def build_demo_manifest(directory):
    """Write fixture audio files plus a manifest; return the manifest path."""
    directory = Path(directory)
    directory.mkdir(parents=True, exist_ok=True)

    sf.write(directory / "dialogue.wav", _dialogue(), 44100, subtype="FLOAT")
    sf.write(directory / "ambience.flac", _ambience(), 32000, subtype="PCM_24")
    sf.write(directory / "combat.wav", _combat(), 48000, subtype="FLOAT")

    manifest = {
        "sample_rate": OUTPUT_RATE,
        "tracks": [
            {"name": "dialogue", "path": "dialogue.wav", "offset_seconds": 0.0,
             "curve": [[0.0, -2.0], [1.0, 0.0], [2.0, -4.0], [2.5, -2.0]]},
            {"name": "ambience", "path": "ambience.flac", "offset_seconds": 0.25,
             "curve": [[0.0, -18.0], [2.5, -12.0]]},
            {"name": "combat", "path": "combat.wav", "offset_seconds": 0.5,
             "curve": [[0.0, 0.0], [0.5, 0.0], [1.5, 6.0], [2.5, 0.0]]},
        ],
    }
    manifest_path = directory / "manifest.json"
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n")
    return manifest_path


if __name__ == "__main__":
    out_dir = sys.argv[1] if len(sys.argv) > 1 else ".run/fixtures"
    print(build_demo_manifest(out_dir))
