#!/usr/bin/env python3
"""Generate deterministic PCM WAV fixtures for audioslice tests and demos.

Default layout (48 kHz mono, 39,360 frames = 0.82 s):

* 0.00–0.10 s  lead-in silence with low-level noise floor;
* 0.10–0.40 s  first content segment (440 Hz tone, 5 ms fades);
* 0.40–0.50 s  inter-segment silence (noise only);
* 0.50–0.80 s  second content segment (554 Hz tone, 5 ms fades);
* 0.80–0.82 s  tail silence (noise only).

With ``--window-ms 20 --min-silence-ms 120`` the detector must return exactly
two segments at samples [4800, 19200) and [24000, 38400) and
``silence_total_seconds = 0.22``.
"""

from __future__ import annotations

import argparse
import os
import sys

import numpy as np

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "src"))

from audioslice.wav import write_pcm_wav  # noqa: E402

DEFAULT_RATE = 48000
NOISE_STD = 6.0
TONE_AMPLITUDE = 2200.0
FADE_MS = 5.0
# (start_sample, end_sample, frequency_hz) for the two content segments.
SEGMENTS = ((4800, 19200, 440.0), (24000, 38400, 554.0))
TOTAL_FRAMES = 39360


def build_signal(seed: int, sample_rate: int = DEFAULT_RATE,
                 channels: int = 1) -> np.ndarray:
    rng = np.random.default_rng(seed)
    mono = rng.normal(0.0, NOISE_STD, TOTAL_FRAMES)
    fade_len = int(round(sample_rate * FADE_MS / 1000.0))
    fade_in = np.linspace(0.0, 1.0, fade_len)
    for start, end, frequency in SEGMENTS:
        idx = np.arange(start, end)
        tone = TONE_AMPLITUDE * np.sin(2.0 * np.pi * frequency * idx / sample_rate)
        tone[:fade_len] *= fade_in
        tone[-fade_len:] *= fade_in[::-1]
        mono[start:end] += tone
    if channels == 1:
        return mono
    # Multi-channel fixtures keep identical content on every channel so the
    # down-mixed detection result stays bit-identical to mono.
    return np.repeat(mono[:, None], channels, axis=1)


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Generate a deterministic audioslice test WAV.")
    parser.add_argument("--out", required=True, help="destination .wav path")
    parser.add_argument("--seed", type=int, default=17, help="RNG seed (default: 17)")
    parser.add_argument("--sample-rate", type=int, default=DEFAULT_RATE)
    parser.add_argument("--channels", type=int, default=1, choices=(1, 2))
    parser.add_argument("--sample-width", type=int, default=2, choices=(1, 2, 3, 4))
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    signal = build_signal(args.seed, args.sample_rate, args.channels)
    write_pcm_wav(args.out, signal, args.sample_rate, args.sample_width)
    print(f"wrote {args.out}: {TOTAL_FRAMES} frames, {args.sample_rate} Hz, "
          f"{args.channels} channel(s), {args.sample_width * 8}-bit PCM, seed={args.seed}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
