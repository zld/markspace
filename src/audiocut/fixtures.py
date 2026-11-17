"""Synthetic PCM WAV fixtures for demos and tests.

Everything is generated locally with NumPy and written through
:class:`audiocut.wavio.WavWriter`; no external samples or network access.
"""

from __future__ import annotations

from pathlib import Path

import numpy as np

from .wavio import WavWriter

DEMO_SAMPLE_RATE = 16000
DEMO_N_SAMPLES = 72_000
DEMO_SPEECH_RANGES = ((4_000, 20_000), (28_000, 44_000), (52_000, 68_000))
DEMO_SEED = 42


def _fade_envelope(length: int, fade_samples: int) -> np.ndarray:
    """Cosine fade-in / fade-out envelope over a tone segment."""

    envelope = np.ones(length, dtype=np.float32)
    n = min(fade_samples, length // 2)
    ramp_in = 0.5 - 0.5 * np.cos(np.linspace(0.0, np.pi, n, dtype=np.float32))
    envelope[:n] = ramp_in
    envelope[-n:] = ramp_in[::-1]
    return envelope


def build_demo_signal(
    n_samples: int = DEMO_N_SAMPLES,
    sample_rate: int = DEMO_SAMPLE_RATE,
    speech_ranges=DEMO_SPEECH_RANGES,
    seed: int = DEMO_SEED,
    noise_rms: float = 0.004,
    speech_rms: float = 0.25,
) -> np.ndarray:
    """Mono float32 signal: low-level noise floor + three fading tone bursts."""

    rng = np.random.default_rng(seed)
    signal = rng.normal(0.0, noise_rms, size=n_samples).astype(np.float32)
    fade_samples = int(0.008 * sample_rate)  # 8 ms fades, shorter than a 20 ms frame
    t = np.arange(n_samples, dtype=np.float32) / sample_rate
    for index, (start, end) in enumerate(speech_ranges):
        length = end - start
        # Different pitch per burst so the three segments are genuinely distinct.
        freq = 220.0 * (index + 1)
        tone = np.sin(2.0 * np.pi * freq * t[start:end]).astype(np.float32)
        tone *= _fade_envelope(length, fade_samples)
        tone *= speech_rms / max(float(np.sqrt(np.mean(tone.astype(np.float64) ** 2))), 1e-9)
        signal[start:end] += tone
    return np.clip(signal, -1.0, 1.0).astype(np.float32)


def write_mono_wav(
    path: str | Path,
    samples: np.ndarray,
    sample_rate: int,
    sample_width: int = 2,
    channels: int = 1,
) -> str:
    """Write a mono signal as PCM WAV (optionally duplicated to stereo)."""

    with WavWriter(path, sample_rate, channels, sample_width) as writer:
        writer.write_mono(np.asarray(samples, dtype=np.float32))
    return str(path)


def create_demo_wav(path: str | Path) -> str:
    """Create the canonical 16 kHz / 72,000-sample demo fixture."""

    signal = build_demo_signal()
    return write_mono_wav(path, signal, DEMO_SAMPLE_RATE, sample_width=2, channels=1)


def create_tone_wav(
    path: str | Path,
    sample_rate: int = 16000,
    duration_s: float = 1.0,
    freq: float = 440.0,
    amplitude: float = 0.3,
    sample_width: int = 2,
    channels: int = 1,
) -> str:
    """A continuous tone (no silence) for tests."""

    n = int(round(duration_s * sample_rate))
    t = np.arange(n, dtype=np.float32) / sample_rate
    samples = (amplitude * np.sin(2 * np.pi * freq * t)).astype(np.float32)
    return write_mono_wav(path, samples, sample_rate, sample_width, channels)


def create_noise_wav(
    path: str | Path,
    sample_rate: int = 16000,
    duration_s: float = 1.0,
    rms: float = 0.003,
    sample_width: int = 2,
    channels: int = 1,
    seed: int = 7,
) -> str:
    """All-silence fixture: only a low noise floor."""

    rng = np.random.default_rng(seed)
    n = int(round(duration_s * sample_rate))
    samples = rng.normal(0.0, rms, size=n).astype(np.float32)
    return write_mono_wav(path, samples, sample_rate, sample_width, channels)
