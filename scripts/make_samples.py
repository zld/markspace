"""Generate deterministic WAV fixtures for local testing.

The output is byte-for-byte reproducible: all randomness comes from a fixed
NumPy generator seed, and no timestamps or external tools are used.
"""

from __future__ import annotations

import argparse
import wave
from pathlib import Path

import numpy as np

SR = 44100
SEED = 13218


def _write_wav(path: Path, samples: np.ndarray, sample_rate: int = SR) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    clipped = np.clip(samples, -1.0, 1.0)
    pcm = np.round(clipped * 32767.0).astype("<i2")
    with wave.open(str(path), "wb") as handle:
        handle.setnchannels(1)
        handle.setsampwidth(2)
        handle.setframerate(sample_rate)
        handle.writeframes(pcm.tobytes())


def _tone(freq: float, duration: float, amp: float, sr: int = SR) -> np.ndarray:
    t = np.arange(int(round(sr * duration)), dtype=np.float64) / sr
    return amp * np.sin(2.0 * np.pi * freq * t)


def _noise(rng: np.random.Generator, duration: float, amp: float, sr: int = SR) -> np.ndarray:
    return rng.normal(0.0, amp, int(round(sr * duration)))


def _silence(duration: float, sr: int = SR) -> np.ndarray:
    return np.zeros(int(round(sr * duration)), dtype=np.float64)


def _concat(*parts: np.ndarray) -> np.ndarray:
    return np.concatenate(parts)


def make_events(rng: np.random.Generator) -> np.ndarray:
    """Weak events, a short pulse, a merged pair, and a slow tail."""
    floor = _noise(rng, 0.6, 0.0004)
    weak = _tone(440.0, 0.18, 0.012) * np.linspace(0.2, 1.0, int(SR * 0.18))
    gap = _noise(rng, 0.15, 0.0004)
    pulse = _tone(900.0, 0.04, 0.15)
    gap2 = _noise(rng, 0.12, 0.0004)
    first = _tone(220.0, 0.08, 0.06)
    seam = _noise(rng, 0.012, 0.0005)
    second = _tone(330.0, 0.08, 0.06)
    gap3 = _noise(rng, 0.2, 0.0004)
    release_len = int(SR * 0.8)
    tail = (
        _tone(160.0, 0.25, 0.08)
        * np.exp(-np.linspace(0.0, 4.0, int(SR * 0.25)))
    )
    tail = _concat(
        tail,
        _tone(160.0, release_len / SR, 0.02)
        * np.exp(-np.linspace(0.0, 5.0, release_len)),
    )
    closing = _noise(rng, 0.5, 0.0004)
    return _concat(
        floor, weak, gap, pulse, gap2, first, seam, second, gap3, tail, closing,
    )


def make_slow_attack(rng: np.random.Generator) -> np.ndarray:
    """A slow swell that must be kept via the low threshold candidate region."""
    lead = _noise(rng, 0.5, 0.0005)
    length = int(SR * 1.2)
    envelope = np.linspace(0.0, 1.0, length) ** 2.5
    swell = 0.25 * np.sin(2.0 * np.pi * 110.0 * np.arange(length) / SR) * envelope
    hold = _tone(110.0, 0.15, 0.25)
    release_len = int(SR * 0.7)
    tail = (
        0.25
        * np.sin(2.0 * np.pi * 110.0 * np.arange(release_len) / SR)
        * np.exp(-np.linspace(0.0, 5.0, release_len))
    )
    close = _noise(rng, 0.6, 0.0005)
    return _concat(lead, swell, hold, tail, close)


def make_background(rng: np.random.Generator) -> np.ndarray:
    """Continuous machinery hum with no events."""
    t = np.arange(int(SR * 2.0), dtype=np.float64) / SR
    hum = (
        0.0012 * np.sin(2.0 * np.pi * 50.0 * t)
        + 0.0008 * np.sin(2.0 * np.pi * 150.0 * t)
        + rng.normal(0.0, 0.0005, t.size)
    )
    return hum


def generate(out: Path) -> list[Path]:
    rng = np.random.default_rng(SEED)
    out.mkdir(parents=True, exist_ok=True)
    created: list[Path] = []
    plan = [
        (out / "events.wav", make_events(rng)),
        (out / "slow_attack.wav", make_slow_attack(rng)),
        (out / "background.wav", make_background(rng)),
    ]
    for path, samples in plan:
        _write_wav(path, samples)
        created.append(path)

    nested = out / "nested" / "quiet_room.wav"
    nested.parent.mkdir(parents=True, exist_ok=True)
    quiet = rng.normal(0.0, 0.0003, SR) * 0.5
    _write_wav(nested, quiet)
    created.append(nested)
    return created


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Generate deterministic WAV samples")
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args(argv)
    created = generate(args.out)
    for path in created:
        print(path)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
