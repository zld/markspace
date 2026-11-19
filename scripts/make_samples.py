"""Generate deterministic sample WAV fixtures for the audioslice pipeline.

All randomness uses a fixed seed so every invocation reproduces byte-identical
files. The generated set covers the tricky cases from the spec:

* ``01_events_with_gaps.wav``   - hum bed, short pause between events and two
                                  closely spaced weak neighbours
* ``02_fades.wav``              - slow fade-in / fade-out tonal events
* ``03_steady_hum.wav``         - continuous low-level noise, no events
* ``04_silence.wav``            - digital silence, edge case for the detector
* ``05_stereo_events.wav``      - two-channel file with a panned event
* ``06_truncated.wav``          - header overstates data size (must be rejected)
* ``07_unsupported.wav``        - 8-bit PCM (must be rejected)
"""

from __future__ import annotations

import argparse
import wave
from pathlib import Path

import numpy as np

SEED = 20261119
SAMPLE_RATE = 48000
DURATION_SECONDS = 3.0
N = int(SAMPLE_RATE * DURATION_SECONDS)


def _hum(rng: np.random.Generator, rms: float = 30.0) -> np.ndarray:
    """Steady air-conditioning-like bed: Gaussian noise plus a low tone."""
    t = np.arange(N) / SAMPLE_RATE
    noise = rng.normal(0.0, rms, N)
    tone = 0.5 * rms * np.sin(2 * np.pi * 90.0 * t)
    return noise + tone


def _tone(t: np.ndarray, freq: float, amp: float) -> np.ndarray:
    return amp * np.sin(2 * np.pi * freq * t)


def build_samples() -> dict[str, np.ndarray]:
    rng = np.random.default_rng(SEED)
    t = np.arange(N) / SAMPLE_RATE
    samples: dict[str, np.ndarray] = {}

    # 1. Events with gaps: hum bed + one isolated event, a short-pause pair,
    #    and two closely spaced *weak* neighbours.
    x = _hum(rng)
    x[(t >= 0.30) & (t < 0.42)] += _tone(t[(t >= 0.30) & (t < 0.42)], 660, 900)
    # short 50 ms pause inside one gesture
    x[(t >= 0.80) & (t < 0.90)] += _tone(t[(t >= 0.80) & (t < 0.90)], 520, 800)
    x[(t >= 0.95) & (t < 1.05)] += _tone(t[(t >= 0.95) & (t < 1.05)], 520, 800)
    # weak neighbours, 60 ms apart (below default 70 ms merge gap)
    weak_a = (t >= 1.60) & (t < 1.70)
    weak_b = (t >= 1.76) & (t < 1.84)
    x[weak_a] += _tone(t[weak_a], 440, 90)
    x[weak_b] += _tone(t[weak_b], 440, 90)
    # another clearly separated event
    x[(t >= 2.30) & (t < 2.40)] += _tone(t[(t >= 2.30) & (t < 2.40)], 760, 700)
    samples["01_events_with_gaps.wav"] = x

    # 2. Fades: two events each with a 200 ms ramp in and out.
    x = _hum(rng)
    for start, end in ((0.4, 1.0), (1.7, 2.4)):
        env = np.zeros(N)
        fi = (t >= start) & (t < start + 0.2)
        hold = (t >= start + 0.2) & (t < end - 0.2)
        fo = (t >= end - 0.2) & (t < end)
        env[fi] = (t[fi] - start) / 0.2
        env[hold] = 1.0
        env[fo] = (end - t[fo]) / 0.2
        x += env * _tone(t, 330, 1200)
    samples["02_fades.wav"] = x

    # 3. Steady hum only: no activity should be reported.
    samples["03_steady_hum.wav"] = _hum(rng)

    # 4. Digital silence: zero activity, full-length silence complement.
    samples["04_silence.wav"] = np.zeros(N)

    # 5. Stereo with an event only in the right channel and a quiet one left.
    t_l_r = np.stack([_hum(rng), _hum(rng)], axis=1)
    mask = (t >= 1.0) & (t < 1.25)
    t_l_r[mask, 1] += _tone(t[mask], 500, 1500)
    t_l_r[mask, 0] += _tone(t[mask], 500, 120)
    samples["05_stereo_events.wav"] = t_l_r

    return samples


def write_pcm16(path: Path, data: np.ndarray, channels: int) -> None:
    pcm = np.clip(data, -32768, 32767).astype("<i2")
    with wave.open(str(path), "wb") as wf:
        wf.setnchannels(channels)
        wf.setsampwidth(2)
        wf.setframerate(SAMPLE_RATE)
        wf.writeframes(pcm.tobytes())


def write_corrupt_files(out_dir: Path) -> None:
    # Truncated: header claims 4000 mono frames but only a few bytes exist.
    trunc = out_dir / "06_truncated.wav"
    with open(trunc, "wb") as fh:
        fh.write(b"RIFF")
        fh.write((36 + 8000).to_bytes(4, "little"))
        fh.write(b"WAVEfmt ")
        fh.write((16).to_bytes(4, "little"))
        fh.write((1).to_bytes(2, "little"))
        fh.write((1).to_bytes(2, "little"))
        fh.write(SAMPLE_RATE.to_bytes(4, "little"))
        fh.write((SAMPLE_RATE * 2).to_bytes(4, "little"))
        fh.write((2).to_bytes(2, "little"))
        fh.write((16).to_bytes(2, "little"))
        fh.write(b"data")
        fh.write((8000).to_bytes(4, "little"))
        fh.write(b"\x00" * 16)

    # Unsupported encoding: 8-bit PCM.
    unsupported = out_dir / "07_unsupported.wav"
    with wave.open(str(unsupported), "wb") as wf:
        wf.setnchannels(1)
        wf.setsampwidth(1)
        wf.setframerate(SAMPLE_RATE)
        wf.writeframes(np.full(N // 8, 128, dtype=np.uint8).tobytes())


def main() -> int:
    parser = argparse.ArgumentParser(description="Generate deterministic audioslice fixtures.")
    parser.add_argument("--out", required=True, help="output directory for sample WAVs")
    args = parser.parse_args()

    out_dir = Path(args.out)
    out_dir.mkdir(parents=True, exist_ok=True)

    for name, data in build_samples().items():
        channels = 1 if data.ndim == 1 else data.shape[1]
        write_pcm16(out_dir / name, data, channels)
    write_corrupt_files(out_dir)

    for path in sorted(out_dir.glob("*.wav")):
        print(f"wrote {path.name} ({path.stat().st_size} bytes)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
