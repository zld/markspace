#!/usr/bin/env python3
"""Deterministically synthesise the WAV fixtures used by tests and demos.

Every sample is generated from code: fixed-seed NumPy noise, sines and
envelopes.  No external audio is referenced.  Re-running the script with
the same arguments produces byte-identical files.

Sets
----
``demo`` (default)
    Three files covering mono/stereo/six-channel material at 44.1 kHz,
    48 kHz and 22.05 kHz.  This is what the acceptance command uses.
``edge``
    Deliberately awkward inputs: zero frames, one frame, a fragment
    shorter than the resampling filter, eight channels, plus rejected
    cases (nine channels, float encoding, 4 kHz rate, corrupt bytes) and
    a nested directory.
"""

from __future__ import annotations

import argparse
import os
import struct
import sys

import numpy as np
import soundfile as sf

__all__ = ["write_demo_set", "write_edge_set", "write_fixtures"]

SEED = 20240501


def _envelope(frames: int, sample_rate: int, attack: float = 0.02, release: float = 0.08) -> np.ndarray:
    """Raised-cosine attack/release envelope, deterministic and click-free."""
    t = np.arange(frames) / sample_rate
    duration = max(t[-1], 1.0 / sample_rate) if frames else 1.0
    attack_n = max(1, int(attack * sample_rate))
    release_n = max(1, int(release * sample_rate))
    env = np.ones(frames)
    if frames:
        a = min(attack_n, frames)
        env[:a] = 0.5 - 0.5 * np.cos(np.linspace(0.0, np.pi, a))
        r = min(release_n, frames)
        env[frames - r :] *= 0.5 - 0.5 * np.cos(np.linspace(np.pi, 0.0, r))
    del duration
    return env


def _lowpass_noise(rng: np.random.Generator, frames: int, taps: int) -> np.ndarray:
    """Deterministic moving-average-smoothed noise."""
    raw = rng.standard_normal(frames + taps)
    kernel = np.ones(taps) / taps
    smoothed = np.convolve(raw, kernel, mode="valid")[:frames]
    peak = np.max(np.abs(smoothed)) if frames else 0.0
    return smoothed / peak if peak > 0.0 else smoothed


def _sine(freq: float, sample_rate: int, frames: int, phase: float = 0.0) -> np.ndarray:
    t = np.arange(frames) / sample_rate
    return np.sin(2.0 * np.pi * freq * t + phase)


def _dialogue(sample_rate: int, frames: int, rng: np.random.Generator) -> np.ndarray:
    """Speech-like material: voiced harmonics with a syllabic envelope."""
    voiced = (
        0.60 * _sine(196.0, sample_rate, frames)
        + 0.25 * _sine(392.0, sample_rate, frames, 0.3)
        + 0.12 * _sine(588.0, sample_rate, frames, 1.1)
        + 0.05 * _sine(1200.0, sample_rate, frames, 0.7)
    )
    breath = _lowpass_noise(rng, frames, 9) * 0.08
    syllable = 0.55 + 0.45 * np.sin(2.0 * np.pi * 3.1 * np.arange(frames) / sample_rate) ** 2
    signal = (voiced + breath) * syllable * _envelope(frames, sample_rate)
    return signal / np.max(np.abs(signal)) * 0.35


def _ambience(sample_rate: int, frames: int, rng: np.random.Generator) -> np.ndarray:
    """Decorrelated stereo room tone with slow swell."""
    channels = []
    for _ in range(2):
        noise = _lowpass_noise(rng, frames, 25)
        swell = 0.6 + 0.4 * np.sin(
            2.0 * np.pi * 0.37 * np.arange(frames) / sample_rate + rng.uniform(0, np.pi)
        )
        channels.append(noise * swell * _envelope(frames, sample_rate, 0.05, 0.15))
    stereo = np.stack(channels, axis=1)
    return stereo / np.max(np.abs(stereo)) * 0.22


def _music(sample_rate: int, frames: int, channels: int = 6) -> np.ndarray:
    """A six-channel chord with per-channel delays and level differences."""
    notes = [130.81, 164.81, 196.00, 261.63, 329.63, 392.00]
    data = np.zeros((frames, channels))
    for ch in range(channels):
        delay = int(round((ch % 3) * 0.004 * sample_rate))
        tone = (
            0.7 * _sine(notes[ch], sample_rate, frames)
            + 0.2 * _sine(notes[ch] * 2.0, sample_rate, frames, 0.4)
            + 0.1 * _sine(notes[ch] * 3.0, sample_rate, frames, 1.2)
        )
        tone = tone * _envelope(frames, sample_rate, 0.01, 0.2)
        if delay:
            tone = np.concatenate((np.zeros(delay), tone[:-delay]))
        data[:, ch] = tone * (0.85 + 0.15 * (ch % 2))
    return data / np.max(np.abs(data)) * 0.30


def _write(path: str, data: np.ndarray, sample_rate: int, subtype: str) -> None:
    os.makedirs(os.path.dirname(path), exist_ok=True)
    sf.write(path, data, sample_rate, subtype=subtype, format="WAV")


def write_demo_set(out_dir: str, seed: int = SEED) -> list[str]:
    """Write the three-file demo set; returns relative paths."""
    rng = np.random.default_rng(seed)
    written = []

    fs = 44100
    frames = int(fs * 1.2)
    path = os.path.join(out_dir, "dialogue_44100_mono.wav")
    _write(path, _dialogue(fs, frames, rng)[:, None], fs, "PCM_16")
    written.append(os.path.basename(path))

    fs = 48000
    frames = int(fs * 1.0)
    path = os.path.join(out_dir, "ambience_48000_stereo.wav")
    _write(path, _ambience(fs, frames, rng), fs, "PCM_24")
    written.append(os.path.basename(path))

    fs = 22050
    frames = int(fs * 1.0)
    path = os.path.join(out_dir, "music_22050_6ch.wav")
    _write(path, _music(fs, frames, channels=6), fs, "PCM_24")
    written.append(os.path.basename(path))
    return written


def write_edge_set(out_dir: str, seed: int = SEED) -> list[str]:
    """Write boundary and rejection fixtures; returns relative paths."""
    rng = np.random.default_rng(seed)
    written = []

    def add(rel: str) -> None:
        written.append(rel.replace(os.sep, "/"))

    # Zero frames, two channels.
    rel = os.path.join("edge", "empty.wav")
    _write(os.path.join(out_dir, rel), np.zeros((0, 2)), 44100, "PCM_16")
    add(rel)

    # Exactly one frame.
    rel = os.path.join("edge", "single_frame.wav")
    _write(os.path.join(out_dir, rel), np.array([[0.25]]), 44100, "PCM_16")
    add(rel)

    # Shorter than one resampling filter window.
    rel = os.path.join("edge", "very_short.wav")
    _write(os.path.join(out_dir, rel), (_sine(440.0, 44100, 16) * 0.5)[:, None], 44100, "PCM_16")
    add(rel)

    # Eight channels, the maximum supported.
    rel = os.path.join("edge", "eight_channels.wav")
    eight = np.stack([_sine(220.0 + 40.0 * ch, 48000, 2400) for ch in range(8)], axis=1) * 0.2
    _write(os.path.join(out_dir, rel), eight, 48000, "PCM_24")
    add(rel)

    # Fully clipped square: the source sits on both PCM rails, so the
    # input clipping count is non-zero, and band-limited interpolation of
    # the discontinuities overshoots beyond [-1, 1] after resampling.
    rel = os.path.join("edge", "hot_square.wav")
    frames = 4410
    hot = np.sign(_sine(110.0, 44100, frames)) * 1.5
    hot = np.clip(hot, -1.0, 1.0)
    _write(os.path.join(out_dir, rel), hot[:, None], 44100, "PCM_16")
    add(rel)

    # Nested directory to prove relative-path preservation.
    rel = os.path.join("nested", "deeper", "leaf.wav")
    _write(
        os.path.join(out_dir, rel),
        (_sine(330.0, 32000, 3200) * 0.4)[:, None],
        32000,
        "PCM_16",
    )
    add(rel)

    # ---- rejected inputs -------------------------------------------------
    rel = os.path.join("invalid", "nine_channels.wav")
    _write(os.path.join(out_dir, rel), np.zeros((480, 9)), 48000, "PCM_16")
    add(rel)

    rel = os.path.join("invalid", "float_encoding.wav")
    _write(os.path.join(out_dir, rel), (_sine(440.0, 48000, 480) * 0.3)[:, None], 48000, "FLOAT")
    add(rel)

    rel = os.path.join("invalid", "low_rate.wav")
    _write(os.path.join(out_dir, rel), (_sine(220.0, 4000, 400) * 0.3)[:, None], 4000, "PCM_16")
    add(rel)

    rel = os.path.join("invalid", "corrupt.wav")
    path = os.path.join(out_dir, rel)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "wb") as fh:
        fh.write(b"RIFF")
        fh.write(struct.pack("<I", 16))
        fh.write(b"WAVEfmt ")
        fh.write(b"\x00" * 8)
    add(rel)
    return written


def write_fixtures(out_dir: str, kind: str = "demo", seed: int = SEED) -> list[str]:
    os.makedirs(out_dir, exist_ok=True)
    written: list[str] = []
    if kind in ("demo", "all"):
        written.extend(write_demo_set(out_dir, seed))
    if kind in ("edge", "all"):
        written.extend(write_edge_set(out_dir, seed))
    return written


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--out", required=True, help="directory to write fixtures into")
    parser.add_argument(
        "--set", dest="kind", choices=("demo", "edge", "all"), default="demo",
        help="which fixture set to write (default: demo)",
    )
    parser.add_argument("--seed", type=int, default=SEED, help=f"RNG seed (default: {SEED})")
    args = parser.parse_args(argv)
    written = write_fixtures(args.out, args.kind, args.seed)
    print(f"wrote {len(written)} fixture(s) to {args.out}")
    for rel in written:
        print(f"  {rel}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
