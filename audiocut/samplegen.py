"""Deterministic sample WAV generator for demos and tests.

Writes three 48 kHz 16-bit mono files:
  combat.wav  5 s: silence, tone, an internal 280 ms gap, tone, silence
  fade.wav    2 s: tone with short (10 ms) fade-in and fade-out
  quiet.wav   0.5 s: low-level (-60 dBFS RMS) noise, no clips at all

Everything is generated from fixed seeds and exact sample counts, so
re-running the generator always produces byte-identical files.
"""
from __future__ import annotations

import os
import wave

import numpy as np

SAMPLE_RATE = 48000
SEED = 14645

# combat.wav layout in sample frames (must stay consistent with docs).
COMBAT_TOTAL = 240000  # 5.00 s
COMBAT_SEG1 = (24000, 91200)  # 0.50 s - 1.90 s
COMBAT_GAP = (91200, 104640)  # 280 ms internal silence
COMBAT_SEG2 = (104640, 167040)  # 2.18 s - 3.48 s

FADE_TOTAL = 96000  # 2.00 s
FADE_LEN = 480  # 10 ms fade-in / fade-out

QUIET_TOTAL = 24000  # 0.50 s
QUIET_SIGMA = 0.001  # -60 dBFS RMS


def _sine(freq: float, n: int, amp: float) -> np.ndarray:
    t = np.arange(n, dtype=np.float64) / SAMPLE_RATE
    return amp * np.sin(2.0 * np.pi * freq * t)


def combat_samples() -> np.ndarray:
    sig = np.zeros(COMBAT_TOTAL, dtype=np.float64)
    a0, a1 = COMBAT_SEG1
    sig[a0:a1] = _sine(440.0, a1 - a0, 0.5)
    b0, b1 = COMBAT_SEG2
    sig[b0:b1] = _sine(660.0, b1 - b0, 0.4)
    return sig


def fade_samples() -> np.ndarray:
    sig = _sine(330.0, FADE_TOTAL, 0.5)
    ramp = np.linspace(0.0, 1.0, FADE_LEN, endpoint=False)
    sig[:FADE_LEN] *= ramp
    sig[FADE_TOTAL - FADE_LEN :] *= ramp[::-1]
    return sig


def quiet_samples() -> np.ndarray:
    rng = np.random.default_rng(SEED)
    return rng.normal(0.0, QUIET_SIGMA, QUIET_TOTAL)


def _to_pcm16(samples: np.ndarray) -> bytes:
    clipped = np.clip(samples, -1.0, 1.0)
    return (clipped * 32767.0).round().astype("<i2").tobytes()


def write_wav_pcm16(path: str, samples: np.ndarray) -> None:
    with wave.open(path, "wb") as wf:
        wf.setnchannels(1)
        wf.setsampwidth(2)
        wf.setframerate(SAMPLE_RATE)
        wf.writeframes(_to_pcm16(samples))


SAMPLES = {
    "combat.wav": combat_samples,
    "fade.wav": fade_samples,
    "quiet.wav": quiet_samples,
}


def generate(out_dir: str) -> list:
    """Write all sample files into out_dir; returns the paths written."""
    os.makedirs(out_dir, exist_ok=True)
    paths = []
    for name in sorted(SAMPLES):
        path = os.path.join(out_dir, name)
        write_wav_pcm16(path, SAMPLES[name]())
        paths.append(path)
    return paths
