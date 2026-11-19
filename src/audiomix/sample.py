"""Deterministic sample-session generator.

``generate-sample`` writes a small session that exercises the whole path:

* ``dialog``  -- mono, 44100 Hz, loud, panned left, dB automation
* ``music``   -- stereo, 48000 Hz, looping region, linear automation
* ``ambience`` -- mono, 22050 Hz, quiet, low static gain

Audio is synthesised with fixed seeds and no randomness beyond seeded
generators, so regenerating overwrites with identical bytes. The manifest is
written last, after every WAV exists.
"""

from __future__ import annotations

import json
from pathlib import Path

import numpy as np
import soundfile as sf

SAMPLE_RATE = 48000
SECONDS = 0.5
DURATION_FRAMES = int(SECONDS * SAMPLE_RATE)


def _tone(seconds: float, rate: int, left_hz: float, right_hz: float,
          amp: float, seed: int) -> np.ndarray:
    """Equal-length-per-second stereo tone with short fades and noise."""
    frames = int(round(seconds * rate))
    rng = np.random.default_rng(seed)
    t = np.arange(frames, dtype=np.float64) / rate
    left = amp * np.sin(2.0 * np.pi * left_hz * t)
    right = amp * np.sin(2.0 * np.pi * right_hz * t)
    fade_in = t / 0.01
    fade_out = (frames - np.arange(frames)) / (rate * 0.05)
    envelope = np.clip(np.minimum(fade_in, fade_out), 0.0, 1.0)
    noise = rng.standard_normal(frames) * 0.002
    return np.stack([(left + noise) * envelope, (right - noise) * envelope], axis=1)


def _add_transient(data: np.ndarray, at: int, amp: float) -> np.ndarray:
    """Add a short, deterministic click so clipping statistics have content."""
    out = np.array(data, copy=True)
    span = min(64, max(0, out.shape[0] - at))
    if span == 0:
        return out
    decay = np.linspace(1.0, 0.0, span) ** 2
    out[at:at + span] += (amp * decay)[:, None]
    return out


def generate_sample(directory: Path) -> Path:
    """Create the demo WAVs and ``session.json``; return the manifest path."""
    directory.mkdir(parents=True, exist_ok=True)
    # dialog: mono 44.1 kHz, loud.
    dialog = _tone(SECONDS, 44100, 220.0, 220.0, 0.85, 11)[:, :1]
    sf.write(str(directory / "dialog.wav"), dialog, 44100, subtype="FLOAT")
    # music: stereo 48 kHz with a loop region and one transient near the
    # start. It pushes both the track and the bus past +-1.0, so the default
    # report policy shows reproducible per-track and master clip counts.
    music = _tone(SECONDS, 48000, 110.0, 164.0, 0.30, 22)
    music = _add_transient(music, 3000, 2.2)
    sf.write(str(directory / "music.wav"), music, 48000, subtype="FLOAT")
    # ambience: mono 22.05 kHz, quiet.
    ambience = _tone(SECONDS, 22050, 55.0, 55.0, 0.05, 33)[:, :1]
    sf.write(str(directory / "ambience.wav"), ambience, 22050, subtype="FLOAT")

    manifest = {
        "channels": 2,
        "sample_rate": SAMPLE_RATE,
        "frames": DURATION_FRAMES,
        "tracks": [
            {
                "name": "dialog",
                "path": "dialog.wav",
                "start_frame": 0,
                "gain_db": -3.0,
                "pan": -0.35,
                "automation": [
                    {"frame": 0, "value": -18.0, "unit": "db"},
                    {"frame": 1200, "value": 0.0, "unit": "db"},
                    {"frame": 20000, "value": -6.0, "unit": "db"},
                ],
            },
            {
                "name": "music",
                "path": "music.wav",
                "start_frame": 500,
                "gain_db": -3.0,
                "loop": {"start_frame": 2000, "end_frame": 18000},
                "automation": [
                    {"frame": 0, "value": 0.4, "unit": "linear"},
                    {"frame": 12000, "value": 0.9, "unit": "linear"},
                    {"frame": 24000, "value": 0.6, "unit": "linear"},
                ],
            },
            {
                "name": "ambience",
                "path": "ambience.wav",
                "start_frame": 0,
                "gain_db": -12.0,
                "automation": [{"frame": 0, "value": -10.0, "unit": "db"}],
            },
        ],
    }
    manifest_path = directory / "session.json"
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    return manifest_path
