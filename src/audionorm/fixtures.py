"""Deterministic sample-audio fixtures, generated in code (no external files).

Every file is a pure function of its parameters plus a fixed seed, so
fixtures are byte-stable across runs and machines.
"""
from __future__ import annotations

from pathlib import Path

import numpy as np
import soundfile as sf

SEED = 20240612


def _tone(rate: int, seconds: float, freqs: list[float], amp: float,
          frames: int | None = None) -> np.ndarray:
    n = frames if frames is not None else int(round(rate * seconds))
    t = np.arange(n, dtype=np.float64) / rate
    cols = []
    for i, f in enumerate(freqs):
        rng = np.random.default_rng(SEED + i)
        noise = 0.02 * rng.standard_normal(n)
        cols.append(amp * np.sin(2.0 * np.pi * f * t) + noise)
    return np.stack(cols, axis=1) if len(cols) > 1 else cols[0][:, None]


def _write(path: Path, data: np.ndarray, rate: int, subtype: str = "PCM_16") -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    sf.write(str(path), data, rate, subtype=subtype)


def generate_all(input_dir: Path) -> list[Path]:
    """Create the standard fixture set; returns the written paths."""
    input_dir.mkdir(parents=True, exist_ok=True)
    written: list[Path] = []

    def add(name: str, data: np.ndarray, rate: int, subtype: str = "PCM_16") -> None:
        p = input_dir / name
        _write(p, data, rate, subtype)
        written.append(p)

    # Stereo 44.1 kHz, odd frame count (not a multiple of the 4096 block).
    add("stereo_tone_44k1.wav", _tone(44100, 1.0, [440.0, 554.0], 0.2, frames=44101), 44100)
    # Mono 48 kHz mid-level tone.
    add("mono_tone_48k.wav", _tone(48000, 0.5, [220.0], 0.3), 48000)
    # Quiet stereo 32 kHz in a subdirectory (recursive scan coverage).
    add("nested/quiet_32k.wav", _tone(32000, 0.75, [330.0, 660.0], 0.01), 32000)
    # Digital silence.
    add("silence.wav", np.zeros((4800, 2)), 48000)
    # Single-frame file.
    add("single_frame.wav", np.array([[0.25, -0.25]]), 44100)
    # Hot float WAV: quiet tone + full-scale impulses. Loudness is low
    # (positive gain demanded) but the true peak exceeds the -1 dBTP
    # ceiling, so the gain limiter must engage; samples beyond full
    # scale give clipped_before > 0.
    hot = _tone(44100, 0.5, [440.0, 440.0], 0.05)
    hot[::4410, :] = 1.15
    add("hot_float.wav", hot, 44100, subtype="FLOAT")
    return written
