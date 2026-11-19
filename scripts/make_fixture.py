"""Generate deterministic WAV fixtures for demos and tests.

The default ``dialogue.wav`` is a 24 kHz mono PCM16 file containing three
spoken-style tone bursts with fade-in/fade-out ramps, a continuous low-level
room tone, one short inter-burst gap (which detection must merge), long
silences between the three lines and a single sample pop.  Everything is
driven by a fixed NumPy seed so repeated runs produce byte-identical output.
"""

from __future__ import annotations

import argparse
import wave
from pathlib import Path

import numpy as np

SAMPLE_RATE = 24000
ROOM_TONE_RMS = 0.002  # ~ -54 dBFS, present even inside "silent" regions


def _tone(duration_samples: int, frequency: float, amplitude: float, fade_samples: int) -> np.ndarray:
    t = np.arange(duration_samples) / SAMPLE_RATE
    wave_data = amplitude * np.sin(2.0 * np.pi * frequency * t)
    envelope = np.ones(duration_samples, dtype=np.float64)
    if fade_samples > 0:
        ramp = np.linspace(0.0, 1.0, fade_samples, dtype=np.float64)
        envelope[:fade_samples] = ramp
        envelope[-fade_samples:] = ramp[::-1]
    return wave_data * envelope


def build_dialogue(seed: int = 20240517) -> np.ndarray:
    """Build the three-line dialogue fixture and return float64 mono samples."""
    rng = np.random.default_rng(seed)
    total = 36000  # 1.5 seconds of audio at 24 kHz
    samples = np.zeros(total, dtype=np.float64)

    # Three ~0.10 s lines whose start samples all sit in the acceptance window
    # of roughly 8000-24000.  Every inter-line gap is at least 230 ms of genuine
    # room-tone-only samples, staying above the 180 ms minimum silence even
    # after sample-accurate edge refinement, so the lines remain separate.
    lines = [
        (8400, 10800, 440.0, 0.35),    # line 1: [8400, 10800), 100 ms
        (16800, 19200, 520.0, 0.30),   # line 2: [16800, 19200), 100 ms
        (24000, 26400, 380.0, 0.32),   # line 3: [24000, 26400), 100 ms
    ]
    fade = 240  # 10 ms fades at 24 kHz
    for start, end, frequency, amplitude in lines:
        end = min(end, total)
        length = end - start
        if length <= 0:
            continue
        samples[start:end] += _tone(length, frequency, amplitude, min(fade, length // 2))

    # A 30 ms low-energy dip inside line 2: far shorter than the 180 ms minimum
    # silence, so detection must bridge it and keep line 2 as a single segment.
    dip_start = 17600
    dip_end = min(dip_start + SAMPLE_RATE // 1000 * 30, total)
    samples[dip_start:dip_end] *= 0.05

    # A short 20 ms blip (a "mouth pop") centered in the long silence between
    # lines 1 and 2.  It is shorter than the 90 ms minimum segment, so the
    # detector must absorb it back into the surrounding silence rather than
    # emit a fourth segment; the silence on either side stays comfortably above
    # the 180 ms minimum even after the blip is removed.
    pop_start, pop_end = 13320, 13320 + SAMPLE_RATE // 1000 * 20
    samples[pop_start:pop_end] = 0.6

    # Continuous room tone over the whole file, including silent regions.
    room = rng.normal(0.0, ROOM_TONE_RMS, size=total)
    samples += room

    return np.clip(samples, -1.0, 1.0)


def write_pcm16(path: str | Path, samples: np.ndarray, sample_rate: int = SAMPLE_RATE) -> None:
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    pcm16 = np.clip(samples, -1.0, 1.0)
    pcm16 = np.round(pcm16 * 32767.0).astype("<i2")
    with wave.open(str(path), "wb") as handle:
        handle.setnchannels(1)
        handle.setsampwidth(2)
        handle.setframerate(sample_rate)
        handle.writeframes(pcm16.tobytes())


def main() -> None:
    parser = argparse.ArgumentParser(description="Generate deterministic audioslice WAV fixtures.")
    parser.add_argument("--out", required=True, help="output WAV path")
    parser.add_argument("--seed", type=int, default=20240517)
    args = parser.parse_args()

    samples = build_dialogue(args.seed)
    destination = Path(args.out)
    write_pcm16(destination, samples)
    print(f"wrote {destination} samples={samples.shape[0]} sample_rate={SAMPLE_RATE}")


if __name__ == "__main__":
    main()
