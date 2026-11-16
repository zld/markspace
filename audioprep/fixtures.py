"""Deterministic WAV fixture generation.

Three samples are synthesized purely from NumPy (no external media), each
exercising a different branch of the pipeline. A fixed random seed makes the
output byte-stable across runs:

1. ``dialogue_mono_44100.wav`` -- mono, 44.1 kHz, clean 1 kHz tone around
   -24 LUFS, below the demonstration -1 dBFS ceiling after normalization;
2. ``ambience_stereo_32000.wav`` -- stereo, 32 kHz, quiet two-tone ambience
   around -30 LUFS, again ceiling-safe;
3. ``battle_stereo_48000.wav`` -- stereo, 48 kHz, loud material whose samples
   reach +/-1.0, forcing the peak ceiling to constrain normalization.

Files are written as PCM_24 WAV (the delivery subtype) so the headers carry
no platform-dependent float state; the deterministic seed keeps the sample
data byte-stable across runs.
"""

from __future__ import annotations

import math
from pathlib import Path

import numpy as np
import soundfile as sf

SEED = 20261117
FIXTURE_SR = 48_000
FIXTURE_SUBTYPE = "PCM_24"


def _tone(
    seconds: float,
    sample_rate: int,
    components: tuple[tuple[float, float], ...],
    amplitude: float,
    rng: np.random.Generator,
) -> np.ndarray:
    """Build a deterministic multi-tone signal with a tiny noise floor."""
    n = int(round(seconds * sample_rate))
    t = np.arange(n, dtype=np.float64) / sample_rate
    signal = np.zeros(n, dtype=np.float64)
    for frequency, weight in components:
        signal += weight * np.sin(2.0 * math.pi * frequency * t)
    signal /= max(1.0, float(np.max(np.abs(signal))))
    # Reproducible, inaudible-level noise avoids an unnaturally exact file.
    signal += 1e-6 * rng.standard_normal(n)
    return amplitude * signal


def _write_wav(path: Path, data: np.ndarray, sample_rate: int) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    sf.write(
        str(path),
        np.asarray(data, dtype=np.float64),
        sample_rate,
        subtype=FIXTURE_SUBTYPE,
        format="WAV",
    )


def generate_fixtures(out_dir: Path) -> list[Path]:
    """Create the three deterministic fixtures; return their paths sorted."""
    out = Path(out_dir)
    rng = np.random.default_rng(SEED)
    written: list[Path] = []

    # 1. Mono dialogue-like tone, 44.1 kHz, ~-24 LUFS, ceiling-safe.
    sr1 = 44_100
    mono = _tone(2.0, sr1, ((440.0, 1.0),), 0.09, rng)
    p1 = out / "dialogue_mono_44100.wav"
    _write_wav(p1, mono, sr1)
    written.append(p1)

    # 2. Quiet stereo ambience, 32 kHz, ~-30 LUFS, ceiling-safe.
    sr2 = 32_000
    left = _tone(1.75, sr2, ((220.0, 0.8), (660.0, 0.4)), 0.045, rng)
    right = _tone(1.75, sr2, ((330.0, 0.8), (880.0, 0.4)), 0.045, rng)
    p2 = out / "ambience_stereo_32000.wav"
    _write_wav(p2, np.stack([left, right], axis=1), sr2)
    written.append(p2)

    # 3. Loud stereo battle material, 48 kHz, samples exactly at +/-1.0.
    sr3 = FIXTURE_SR
    left = _tone(1.5, sr3, ((80.0, 1.0), (2400.0, 0.5)), 1.0, rng)
    right = _tone(1.5, sr3, ((120.0, 1.0), (3200.0, 0.5)), 1.0, rng)
    p3 = out / "battle_stereo_48000.wav"
    _write_wav(p3, np.stack([left, right], axis=1), sr3)
    written.append(p3)

    return sorted(written)
