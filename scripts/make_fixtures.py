#!/usr/bin/env python3
"""Generate deterministic sample WAV fixtures for audioprep.

All content is synthesized from fixed seeds; no external files are read.
Default fixtures are all valid so a demo batch exits 0; --with-errors adds a
corrupt file and a symlink loop to exercise failure and discovery paths.
"""
from __future__ import annotations

import argparse
import os
from pathlib import Path

import numpy as np
import soundfile as sf

SEED = 20240613


def _t(rate: int, seconds: float) -> np.ndarray:
    return np.arange(int(rate * seconds), dtype=np.float64) / rate


def _write(path: Path, data: np.ndarray, rate: int) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    sf.write(path, np.atleast_2d(data.T).T if data.ndim == 1 else data,
             rate, subtype="PCM_24")
    print(f"wrote {path}")


def build(out: Path, with_errors: bool) -> None:
    rng = np.random.default_rng(SEED)

    # Stereo 44.1 kHz, moderate level -> ~8.6 dB boost to -18 LUFS.
    t = _t(44100, 1.5)
    left = 0.05 * np.sin(2 * np.pi * 997.0 * t)
    right = 0.04 * np.sin(2 * np.pi * 1497.0 * t + 0.3) \
        + 0.001 * rng.standard_normal(t.size)
    _write(out / "tone_stereo_44k1.wav", np.stack([left, right], 1), 44100)

    # Quiet mono 22.05 kHz -> required boost exceeds the 12 dB cap.
    t = _t(22050, 1.0)
    _write(out / "quiet_mono_22k.wav", 0.004 * np.sin(2 * np.pi * 440.0 * t),
           22050)

    # Digital silence -> non-normalizable, stays silent.
    _write(out / "silence_stereo_48k.wav", np.zeros((24000, 2)), 48000)

    # Four-channel 96 kHz, one sine per channel.
    t = _t(96000, 0.8)
    quad = np.stack([0.08 * np.sin(2 * np.pi * f * t)
                     for f in (330.0, 660.0, 990.0, 1320.0)], axis=1)
    _write(out / "quad_96k.wav", quad, 96000)

    # Shorter than any filter: 40 frames.
    t = _t(44100, 40 / 44100.0)
    _write(out / "short_44k1.wav", 0.1 * np.sin(2 * np.pi * 1000.0 * t),
           44100)

    # Nested relative paths must be mirrored into the output tree.
    t = _t(48000, 1.0)
    nested = np.stack([0.06 * np.sin(2 * np.pi * 880.0 * t),
                       0.06 * np.sin(2 * np.pi * 1320.0 * t)], axis=1)
    _write(out / "nested" / "deep" / "tone_48k.wav", nested, 48000)

    # Sparse near-full-scale spikes: low LUFS, high true peak -> backoff.
    spiky = np.zeros(48000)
    spiky[::4800] = 0.9
    _write(out / "spiky_48k.wav", spiky, 48000)

    if with_errors:
        corrupt = out / "corrupt.wav"
        corrupt.write_bytes(b"RIFF" + b"\x00" * 4 + b"WAVEnot-a-real-wav")
        print(f"wrote {corrupt}")
        loop = out / "loop"
        if not loop.exists():
            os.symlink(".", loop)
            print(f"linked {loop} -> .")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", required=True, help="output directory")
    parser.add_argument("--with-errors", action="store_true",
                        help="also emit a corrupt WAV and a symlink loop")
    args = parser.parse_args()
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    build(out, args.with_errors)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
