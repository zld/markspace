#!/usr/bin/env python3
"""Generate deterministic PCM WAV fixtures for audiosplit demos and tests."""
from __future__ import annotations

import argparse
import json
import struct
import sys
from pathlib import Path

import numpy as np

SAMPLE_FORMAT = 1
BITS = 16


def write_pcm16(path: Path, samples: np.ndarray, sample_rate: int) -> None:
    """Write ``(frames, channels)`` float samples as 16-bit PCM WAV."""
    if samples.ndim == 1:
        samples = samples[:, None]
    clipped = np.clip(samples, -1.0, 1.0)
    pcm = np.round(clipped * 32767.0).astype("<i2")
    channels = pcm.shape[1]
    block_align = channels * 2
    data = pcm.tobytes()
    fmt = struct.pack(
        "<HHIIHH", SAMPLE_FORMAT, channels, sample_rate,
        sample_rate * block_align, block_align, BITS,
    )
    chunks = b"fmt " + struct.pack("<I", len(fmt)) + fmt
    chunks += b"data" + struct.pack("<I", len(data)) + data
    header = b"RIFF" + struct.pack("<I", 4 + len(chunks)) + b"WAVE"
    path.write_bytes(header + chunks)


def noise(rng: np.random.Generator, size: int, level: float) -> np.ndarray:
    return rng.normal(0.0, level, size).astype(np.float32)


def tone(size: int, freq: float, rate: int) -> np.ndarray:
    t = np.arange(size, dtype=np.float64) / rate
    return np.sin(2.0 * np.pi * freq * t).astype(np.float32)


def shaped(size: int, rate: int, fade_in_ms: float, fade_out_ms: float) -> np.ndarray:
    env = np.ones(size, dtype=np.float32)
    fi = min(size, int(rate * fade_in_ms / 1000.0))
    fo = min(size - fi, int(rate * fade_out_ms / 1000.0))
    if fi:
        env[:fi] = np.linspace(0.0, 1.0, fi, dtype=np.float32)
    if fo:
        env[size - fo :] = np.linspace(1.0, 0.0, fo, dtype=np.float32)
    return env


def add_burst(
    buffer: np.ndarray,
    start: int,
    length: int,
    rate: int,
    freq: float,
    amplitude: float,
    fade_in_ms: float = 15.0,
    fade_out_ms: float = 15.0,
) -> None:
    burst = tone(length, freq, rate) * amplitude
    burst *= shaped(length, rate, fade_in_ms, fade_out_ms)
    stop = min(buffer.shape[0], start + length)
    buffer[start:stop] += burst[: stop - start]


def make_tone_bursts(path: Path) -> dict[str, object]:
    rate = 48000
    length = int(2.4 * rate)
    rng = np.random.default_rng(1701)
    signal = noise(rng, length, 0.004)
    add_burst(signal, int(0.25 * rate), int(0.35 * rate), rate, 440.0, 0.20, 25, 25)
    add_burst(signal, int(1.35 * rate), int(0.50 * rate), rate, 660.0, 0.12, 40, 60)
    write_pcm16(path, signal, rate)
    return {"file": path.name, "sample_rate": rate, "channels": 1,
            "samples": length, "events": 2}


def make_stereo_pulses(path: Path) -> dict[str, object]:
    rate = 44100
    length = int(1.8 * rate)
    rng = np.random.default_rng(2903)
    left = noise(rng, length, 0.003)
    right = noise(rng, length, 0.0035)
    add_burst(left, int(0.30 * rate), int(0.07 * rate), rate, 1200.0, 0.30, 8, 8)
    add_burst(right, int(0.30 * rate), int(0.07 * rate), rate, 1200.0, 0.22, 8, 8)
    add_burst(left, int(0.95 * rate), int(0.06 * rate), rate, 800.0, 0.16, 8, 8)
    add_burst(right, int(0.95 * rate), int(0.06 * rate), rate, 800.0, 0.28, 8, 8)
    stereo = np.stack([left, right], axis=1)
    write_pcm16(path, stereo, rate)
    return {"file": path.name, "sample_rate": rate, "channels": 2,
            "samples": length, "events": 2}


def make_fade_and_tail(path: Path) -> dict[str, object]:
    rate = 48000
    length = int(1.6 * rate)
    rng = np.random.default_rng(4127)
    signal = noise(rng, length, 0.003)
    # Slow symmetric fade tests threshold hysteresis on both edges.
    add_burst(signal, int(0.10 * rate), int(0.55 * rate), rate, 220.0, 0.18, 220, 220)
    # Second event runs into EOF and must be closed at the final sample.
    add_burst(signal, int(1.20 * rate), length - int(1.20 * rate), rate,
              520.0, 0.25, 15, 0.0)
    write_pcm16(path, signal, rate)
    return {"file": path.name, "sample_rate": rate, "channels": 1,
            "samples": length, "events": 2}


def make_invalid(directory: Path) -> list[dict[str, str]]:
    """Bad inputs live in a subdirectory so a non-recursive scan skips them."""
    directory.mkdir(parents=True, exist_ok=True)
    cases: list[dict[str, str]] = []
    empty = directory / "empty.wav"
    empty.write_bytes(b"")
    cases.append({"file": f"invalid/{empty.name}", "reason": "empty file"})
    truncated = directory / "truncated.wav"
    good = directory.parent / "tone_bursts_mono.wav"
    truncated.write_bytes(good.read_bytes()[:-37])
    cases.append({"file": f"invalid/{truncated.name}", "reason": "truncated data"})
    bad_fmt = directory / "not_pcm.wav"
    body = (good.read_bytes()[:20]
            + struct.pack("<H", 3)
            + good.read_bytes()[22:])
    bad_fmt.write_bytes(body)
    cases.append({"file": f"invalid/{bad_fmt.name}", "reason": "non-PCM format"})
    return cases


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", type=Path, default=Path(".tmp-fixtures"))
    args = parser.parse_args(argv)
    out = args.out
    out.mkdir(parents=True, exist_ok=True)
    manifest = [
        make_tone_bursts(out / "tone_bursts_mono.wav"),
        make_stereo_pulses(out / "stereo_pulses.wav"),
        make_fade_and_tail(out / "fade_and_tail.wav"),
    ]
    invalid = make_invalid(out / "invalid")
    payload = {"files": manifest, "invalid": invalid}
    (out / "manifest.json").write_text(
        json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    print(f"wrote {len(manifest)} fixtures and {len(invalid)} invalid cases to {out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
