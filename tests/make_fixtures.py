"""Deterministically generate WAV fixtures for the soundindex pipeline.

Samples embed AC/room noise floors, quiet tails, fade in/out swells and
pure silence. No external files or network access are required.

Usage: python tests/make_fixtures.py --out /tmp/sound-fixtures [--with-bad]
"""
from __future__ import annotations

import argparse
import os
import struct
import wave

import numpy as np

SR = 16000
SEED = 20260401


def _write_wav(path: str, samples: np.ndarray, sr: int = SR) -> None:
    pcm = np.clip(samples, -1.0, 1.0)
    pcm16 = (pcm * 32767.0).round().astype("<i2")
    channels = 1 if pcm16.ndim == 1 else pcm16.shape[1]
    with wave.open(path, "wb") as w:
        w.setnchannels(channels)
        w.setsampwidth(2)
        w.setframerate(sr)
        w.writeframes(pcm16.tobytes())


def _tone(sr: int, n: int, freq: float) -> np.ndarray:
    t = np.arange(n) / sr
    return np.sin(2 * np.pi * freq * t) + 0.5 * np.sin(2 * np.pi * 2 * freq * t)


def _fade(x: np.ndarray, n: int) -> np.ndarray:
    env = np.ones(len(x))
    env[:n] = np.linspace(0.0, 1.0, n)
    env[-n:] = np.linspace(1.0, 0.0, n)
    return x * env


def build_fixtures(out_dir: str) -> list:
    rng = np.random.default_rng(SEED)
    os.makedirs(out_dir, exist_ok=True)

    # 1) two voice-like bursts over an AC noise floor, with 30 ms fades
    n = 6 * SR
    sig = rng.normal(0.0, 0.010, n)
    for start_s, dur_s in ((0.8, 1.6), (3.6, 1.4)):
        a = int(start_s * SR)
        b = a + int(dur_s * SR)
        sig[a:b] += _fade(0.45 * _tone(SR, b - a, 180.0), int(0.03 * SR))
    _write_wav(os.path.join(out_dir, "voice_with_noise.wav"), sig)

    # 2) loud body with a quiet tail that the gate must not cut off
    n = 4 * SR
    sig = rng.normal(0.0, 0.008, n)
    seg = int(2.2 * SR)
    body = int(1.5 * SR)
    env = np.concatenate([np.full(body, 0.4), np.linspace(0.4, 0.03, seg - body)])
    sig[SR // 2: SR // 2 + seg] += _tone(SR, seg, 220.0) * env
    _write_wav(os.path.join(out_dir, "quiet_tail.wav"), sig)

    # 3) stereo crescendo/decrescendo swell at 44.1 kHz
    sr2 = 44100
    n = 5 * sr2
    t = np.arange(n) / sr2
    env = np.clip(1.0 - np.abs(t - 2.5) / 2.0, 0.02, 1.0) * 0.5
    left = np.sin(2 * np.pi * 330.0 * t) * env + rng.normal(0.0, 0.005, n)
    right = np.sin(2 * np.pi * 332.0 * t) * env + rng.normal(0.0, 0.005, n)
    _write_wav(os.path.join(out_dir, "swell_stereo.wav"),
               np.stack([left, right], axis=1), sr2)

    # 4) pure low-level room tone: zero clips, silence ~= full duration
    sig = rng.normal(0.0, 0.004, 3 * SR)
    _write_wav(os.path.join(out_dir, "silence.wav"), sig)

    return ["voice_with_noise.wav", "quiet_tail.wav",
            "swell_stereo.wav", "silence.wav"]


def build_bad_fixtures(out_dir: str, good_wav: str) -> list:
    """Corrupted inputs under <out_dir>/bad/ (a subdir, so a plain
    directory scan of out_dir never picks them up)."""
    bad = os.path.join(out_dir, "bad")
    os.makedirs(bad, exist_ok=True)

    open(os.path.join(bad, "empty.wav"), "wb").close()

    with open(good_wav, "rb") as fh:
        data = fh.read()
    with open(os.path.join(bad, "truncated.wav"), "wb") as fh:
        fh.write(data[: len(data) * 3 // 5])

    # minimal RIFF with format tag 3 (IEEE float) -> non-PCM for wave module
    payload = b"\x00" * 16
    fmt = struct.pack("<HHIIHH", 3, 1, SR, SR * 4, 4, 32)
    riff = b"WAVEfmt " + struct.pack("<I", len(fmt)) + fmt + \
        b"data" + struct.pack("<I", len(payload)) + payload
    with open(os.path.join(bad, "nonpcm.wav"), "wb") as fh:
        fh.write(b"RIFF" + struct.pack("<I", len(riff)) + riff)

    with open(os.path.join(bad, "notawav.wav"), "w", encoding="utf-8") as fh:
        fh.write("this is not a wav file at all\n")
    return ["empty.wav", "truncated.wav", "nonpcm.wav", "notawav.wav"]


def main(argv=None) -> int:
    ap = argparse.ArgumentParser(description="generate deterministic WAV fixtures")
    ap.add_argument("--out", required=True, help="output directory")
    ap.add_argument("--with-bad", action="store_true",
                    help="also emit corrupted samples under <out>/bad/")
    args = ap.parse_args(argv)
    names = build_fixtures(args.out)
    for name in names:
        print(f"wrote {os.path.join(args.out, name)}")
    if args.with_bad:
        good = os.path.join(args.out, "voice_with_noise.wav")
        for name in build_bad_fixtures(args.out, good):
            print(f"wrote {os.path.join(args.out, 'bad', name)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
