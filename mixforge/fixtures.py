"""Deterministic demo fixture generation -- no external assets involved.

Three 1.2 s tracks at different sample rates (32 kHz mono sfx,
44.1 kHz mono voice, 48 kHz stereo ambience) are synthesized from a
seeded RNG and written as float WAVs together with a manifest.  The
``stress`` flag adds a master-bus boost so the summed mix exceeds
+0 dBFS and the clip counter has something to count.
"""

from __future__ import annotations

import json
from pathlib import Path

import numpy as np
import soundfile as sf

DURATION_S = 1.2
STRESS_MASTER_DB = 2.3  # applied on the bus when --stress is passed


def _tone(rng: np.random.Generator, rate: int, freq: float, amp: float,
          extra: tuple[tuple[float, float], ...] = ()) -> np.ndarray:
    n = int(round(rate * DURATION_S))
    t = np.arange(n, dtype=np.float64) / rate
    sig = amp * np.sin(2.0 * np.pi * freq * t)
    for f2, a2 in extra:
        sig = sig + a2 * np.sin(2.0 * np.pi * f2 * t + rng.uniform(0.0, 2.0 * np.pi))
    return sig


def make_demo_project(directory: str | Path, seed: int = 0,
                      stress: bool = False) -> tuple[Path, float]:
    """Write demo WAVs + manifest into ``directory``.

    Returns ``(manifest_path, master_gain_db)``; the master gain is the
    stress boost (0 dB when ``stress`` is false).
    """
    directory = Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    rng = np.random.default_rng(seed)

    sfx = _tone(rng, 32000, 200.0, 0.38, extra=((600.0, 0.04),))
    voice = _tone(rng, 44100, 330.0, 0.33, extra=((495.0, 0.045),))
    amb_l = _tone(rng, 48000, 150.0, 0.37)
    amb_r = _tone(rng, 48000, 151.5, 0.37)
    amb = np.column_stack((amb_l, amb_r))

    for name, data, rate in (("sfx", sfx, 32000), ("voice", voice, 44100), ("amb", amb, 48000)):
        sf.write(str(directory / f"{name}.wav"), data, rate, subtype="FLOAT")

    manifest = {
        "target_rate": 48000,
        "tracks": [
            {
                "name": "sfx",
                "path": "sfx.wav",
                "gain_db": 0.0,
                "pan": 0.0,
                "interp": "cubic",
                "automation": [
                    {"frame": 0, "db": -2.0},
                    {"frame": 28800, "db": 0.0},
                    {"frame": 57599, "db": -2.0},
                ],
            },
            {"name": "voice", "path": "voice.wav", "gain_db": 0.0, "pan": -0.25},
            {"name": "amb", "path": "amb.wav", "gain_db": 0.0, "pan": 0.0},
        ],
    }
    manifest_path = directory / "manifest.json"
    manifest_path.write_text(json.dumps(manifest, indent=2), encoding="utf-8")
    return manifest_path, STRESS_MASTER_DB if stress else 0.0
