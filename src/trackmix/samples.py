"""Deterministic synthetic audio for demos and tests.

Everything is derived from an integer seed plus a per-track ordinal, so
re-running with the same arguments regenerates byte-identical WAV files.
Four signal flavours cover typical game content: dialog-like pitched
bursts, music-like summed sines, ambient noise and a loud impulse track
used to force full-scale clipping under ``--stress-clip``.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

import numpy as np
import soundfile as sf

from .errors import WriteError

FLAVOURS = ("dialog", "music", "ambient", "impulse")


@dataclass(frozen=True)
class GeneratedTrack:
    path: Path
    rate: int
    frames: int
    channels: int
    flavour: str
    seed: int


def _rng(seed: int, ordinal: int) -> np.random.Generator:
    # Independent, stable stream per track regardless of generation order.
    return np.random.default_rng(np.int64(seed) * 1_000_003 + ordinal * 97 + 17)


def _dialog(n: int, rng: np.random.Generator) -> np.ndarray:
    t = np.arange(n, dtype=np.float64)
    carrier = 0.5 * np.sin(2 * np.pi * 180.0 * t / 48000.0)
    # Slow amplitude envelope mimics speech phrasing.
    envelope = 0.5 + 0.5 * np.sin(2 * np.pi * (1.7 + rng.uniform(0, 0.4)) * t / 48000.0)
    return carrier * envelope


def _music(n: int, rng: np.random.Generator) -> np.ndarray:
    t = np.arange(n, dtype=np.float64)
    base = rng.choice([220.0, 261.63, 329.63])
    signal = (
        0.34 * np.sin(2 * np.pi * base * t / 48000.0)
        + 0.22 * np.sin(2 * np.pi * base * 1.5 * t / 48000.0)
        + 0.12 * np.sin(2 * np.pi * base * 2.0 * t / 48000.0)
    )
    fade_len = min(n, 2048)
    fade = np.ones(n, dtype=np.float64)
    fade[:fade_len] = np.linspace(0.0, 1.0, fade_len)
    return signal * fade


def _ambient(n: int, rng: np.random.Generator) -> np.ndarray:
    # White noise through a one-pole low-pass for a wind-like bed.
    white = rng.normal(0.0, 0.5, size=n)
    out = np.empty(n, dtype=np.float64)
    prev = 0.0
    coeff = 0.92
    for i in range(n):
        prev = coeff * prev + (1.0 - coeff) * white[i]
        out[i] = prev
    return out * 2.2


def _impulse(n: int, rng: np.random.Generator) -> np.ndarray:
    # Sparse loud impulses — guaranteed overload material when summed.
    signal = np.zeros(n, dtype=np.float64)
    spacing = max(1, n // 8)
    for i in range(3, n, spacing):
        signal[i] = rng.choice([-1.0, 1.0])
    # A little tone around each impulse so it is not a bare click only.
    tone = 0.3 * np.sin(2 * np.pi * 880.0 * np.arange(n) / 48000.0)
    return signal + tone


_BUILDERS = {
    "dialog": _dialog,
    "music": _music,
    "ambient": _ambient,
    "impulse": _impulse,
}


def generate_track(
    directory: Path,
    seed: int,
    ordinal: int,
    rate: int,
    frames: int,
    channels: int,
    flavour: str,
    stress_clip: bool,
) -> GeneratedTrack:
    """Render one deterministic WAV and return its description."""
    if flavour not in _BUILDERS:
        raise ValueError(f"unknown flavour {flavour!r}")
    rng = _rng(seed, ordinal)
    mono = _BUILDERS[flavour](frames, rng)
    if channels > 1:
        # Deterministic per-channel decorrelation, bounded amplitude.
        tails = rng.normal(0.0, 0.05, size=(frames, channels - 1))
        data = np.concatenate(
            (mono.reshape(-1, 1), mono.reshape(-1, 1) + tails), axis=1
        )
    else:
        data = mono.reshape(-1, 1)
    if stress_clip:
        # Push hot levels so the bus sum exceeds full scale somewhere.
        data = data * 1.6 + 0.35
    data = np.clip(data, -8.0, 8.0)
    if not np.all(np.isfinite(data)):  # pragma: no cover - generator guard
        raise WriteError(f"generated track {ordinal} produced non-finite samples")

    directory = Path(directory)
    try:
        directory.mkdir(parents=True, exist_ok=True)
    except OSError as exc:
        raise WriteError(
            f"cannot create source directory {directory}: {exc}"
        ) from exc
    path = directory / f"src_{ordinal:02d}_{flavour}_{rate}.wav"
    try:
        sf.write(str(path), data.astype(np.float32), rate, subtype="FLOAT",
                 format="WAV")
    except (OSError, sf.LibsndfileError) as exc:
        raise WriteError(f"failed to write generated WAV {path}: {exc}") from exc
    return GeneratedTrack(path, rate, frames, channels, flavour, seed)
