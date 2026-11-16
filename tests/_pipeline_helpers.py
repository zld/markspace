"""Shared helpers for the pipeline test modules."""

from __future__ import annotations

from pathlib import Path

import numpy as np
import soundfile as sf

PARAMS = dict(
    sample_rate=48000,
    channels="stereo",
    target_lufs=-18.0,
    peak_ceiling=-1.0,
    block_frames=4096,
    workers=1,
)


def write_wav(path: Path, data: np.ndarray, sr: int, subtype: str = "PCM_24"):
    path.parent.mkdir(parents=True, exist_ok=True)
    sf.write(str(path), data, sr, subtype=subtype, format="WAV")


def make_inputs(root: Path) -> None:
    sr = 44100
    t = np.arange(int(2 * sr)) / sr
    mono = 0.1 * np.sin(2 * np.pi * 440 * t)
    write_wav(root / "a_mono.wav", mono, sr)
    stereo = np.stack(
        [0.08 * np.sin(2 * np.pi * 330 * t), 0.08 * np.sin(2 * np.pi * 660 * t)],
        axis=1,
    )
    write_wav(root / "nested" / "b_stereo.wav", stereo, 32000)


def batch_args(in_dir: Path, out_dir: Path, report: Path, **overrides):
    args = dict(input_dir=in_dir, output_dir=out_dir, report=report)
    args.update(PARAMS)
    args.update(overrides)
    return args
