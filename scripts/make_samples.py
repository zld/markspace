"""Generate deterministic sample WAV fixtures.

Creates four files under --out:
  dialogue_32k.wav   two speech-like bursts with a long separating silence
  ambience_48k.wav   sustained ambience with slow fade-in and fade-out tails
  interactive_44100_stereo.wav  stereo UI blips with short pauses
  broken_truncated.wav          header claims more frames than the data holds

All samples come from a seeded numpy generator; reruns are byte-identical.
"""
from __future__ import annotations

import argparse
import wave
from pathlib import Path

import numpy as np

SEED = 20261119


def _tone_burst(rng: np.random.Generator, n: int, sr: int, freq: float,
                amp: float) -> np.ndarray:
    """Speech-like burst: shaped tone with breath noise and fade edges."""
    t = np.arange(n) / sr
    env = np.hanning(n)
    tone = amp * np.sin(2 * np.pi * freq * t) * env
    breath = rng.normal(0, amp * 0.08, n) * env
    return (tone + breath).astype(np.float32)


def _noise(rng: np.random.Generator, n: int, amp: float) -> np.ndarray:
    return rng.normal(0, amp, n).astype(np.float32)


def _write_pcm(path: Path, samples: np.ndarray, sr: int, width: int = 2,
               channels: int = 1) -> None:
    if samples.ndim == 1:
        samples = samples.reshape(-1, 1)
    interleaved = samples.reshape(-1)
    if width == 2:
        pcm = np.clip(interleaved, -1.0, 1.0 - 1 / 32768)
        raw = (pcm * 32768).astype("<i2").tobytes()
    else:
        pcm = np.clip(interleaved, -1.0, 1.0 - 1 / 128)
        raw = ((pcm * 128) + 128).astype(np.uint8).tobytes()
    with wave.open(str(path), "wb") as wf:
        wf.setnchannels(channels)
        wf.setsampwidth(width)
        wf.setframerate(sr)
        wf.writeframes(raw)


def build(out_dir: Path) -> list[str]:
    rng = np.random.default_rng(SEED)
    out_dir.mkdir(parents=True, exist_ok=True)
    created: list[str] = []

    # 1) Dialogue: 0.4s floor, 0.9s burst, 0.7s silence, 0.8s burst, 0.4s floor
    sr = 32000
    dialogue = np.concatenate([
        _noise(rng, int(sr * 0.4), 0.004),
        _tone_burst(rng, int(sr * 0.9), sr, 320.0, 0.35),
        _noise(rng, int(sr * 0.7), 0.004),
        _tone_burst(rng, int(sr * 0.8), sr, 240.0, 0.25),
        _noise(rng, int(sr * 0.4), 0.004),
    ])
    p = out_dir / "dialogue_32k.wav"
    _write_pcm(p, dialogue, sr)
    created.append(p.name)

    # 2) Ambience: 6s with 0.5s fade-in, steady bed and 0.5s fade-out
    sr = 48000
    n = int(sr * 6.0)
    bed = _noise(rng, n, 0.12)
    fade_n = int(sr * 0.5)
    bed[:fade_n] *= np.linspace(0.0, 1.0, fade_n)
    bed[-fade_n:] *= np.linspace(1.0, 0.0, fade_n)
    p = out_dir / "ambience_48k.wav"
    _write_pcm(p, bed, sr)
    created.append(p.name)

    # 3) Stereo interactive: two blips with a 0.15s pause, weak second blip
    sr = 44100
    blip1 = _tone_burst(rng, int(sr * 0.25), sr, 880.0, 0.3)
    pause = _noise(rng, int(sr * 0.15), 0.003)
    blip2 = _tone_burst(rng, int(sr * 0.2), sr, 1320.0, 0.06)
    lead_in = _noise(rng, int(sr * 0.3), 0.003)
    lead_out = _noise(rng, int(sr * 0.3), 0.003)
    mono = np.concatenate([lead_in, blip1, pause, blip2, lead_out])
    stereo = np.column_stack([mono, 0.7 * mono])
    p = out_dir / "interactive_44100_stereo.wav"
    _write_pcm(p, stereo, sr, channels=2)
    created.append(p.name)

    # 4) Corrupt: valid header promising 2s, but data chunk truncated to half
    sr = 16000
    dummy = _noise(rng, sr, 0.1)
    good = out_dir / "_tmp_good.wav"
    _write_pcm(good, dummy, sr)
    raw = good.read_bytes()
    good.unlink()
    p = out_dir / "broken_truncated.wav"
    p.write_bytes(raw[: len(raw) // 2])
    created.append(p.name)

    return created


def main() -> None:
    parser = argparse.ArgumentParser(description="Generate deterministic WAV fixtures")
    parser.add_argument("--out", required=True, help="output directory")
    args = parser.parse_args()
    created = build(Path(args.out))
    for name in created:
        print(f"created {name}")


if __name__ == "__main__":
    main()
