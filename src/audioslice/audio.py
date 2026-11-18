"""PCM sample generation and reading.

Only 16-bit signed PCM inside a RIFF/WAVE container is supported (read via
the stdlib ``wave`` module -- no external converter is ever invoked). Mono
and stereo are accepted; stereo is mixed down by averaging channels, so a
sample index always refers to the same instant in the original file.
"""
from __future__ import annotations

import wave
from dataclasses import dataclass
from pathlib import Path

import numpy as np

MIN_SAMPLE_RATE = 8_000
MAX_SAMPLE_RATE = 96_000
SAMPWIDTH_BYTES = 2  # 16-bit PCM is the only supported sample format
_FULL_SCALE = float(1 << 15)


class AudioError(Exception):
    """File-level audio problem: unsupported format, corrupt data, empty file."""


@dataclass(frozen=True)
class AudioInfo:
    path: Path
    sample_rate: int
    channels: int
    num_samples: int  # per channel

    @property
    def duration_seconds(self) -> float:
        return self.num_samples / self.sample_rate


def read_info(path) -> AudioInfo:
    """Validate a PCM file and return its header info, or raise AudioError."""
    path = Path(path)
    try:
        with wave.open(str(path), "rb") as wf:
            width = wf.getsampwidth()
            channels = wf.getnchannels()
            rate = wf.getframerate()
            frames = wf.getnframes()
    except (wave.Error, EOFError, OSError) as exc:
        raise AudioError(f"{path.name}: not a readable PCM wave file: {exc}") from exc
    if width != SAMPWIDTH_BYTES:
        raise AudioError(
            f"{path.name}: unsupported bit depth ({width * 8}-bit); "
            "only 16-bit PCM is supported"
        )
    if channels not in (1, 2):
        raise AudioError(f"{path.name}: unsupported channel count {channels}")
    if not MIN_SAMPLE_RATE <= rate <= MAX_SAMPLE_RATE:
        raise AudioError(
            f"{path.name}: sample rate {rate} Hz outside "
            f"{MIN_SAMPLE_RATE}-{MAX_SAMPLE_RATE} Hz"
        )
    if frames <= 0:
        raise AudioError(f"{path.name}: empty file (no samples)")
    return AudioInfo(path=path, sample_rate=rate, channels=channels, num_samples=frames)


def _decode(raw: bytes, channels: int) -> np.ndarray:
    samples = np.frombuffer(raw, dtype="<i2")
    if samples.size % channels:
        raise AudioError("corrupt frame data: sample count not divisible by channels")
    frames = samples.reshape(-1, channels).astype(np.float32) / _FULL_SCALE
    return frames.mean(axis=1)


def iter_mono(path, chunk_samples: int = 1 << 18):
    """Yield mono float32 chunks; indices map 1:1 onto the source samples."""
    try:
        with wave.open(str(path), "rb") as wf:
            channels = wf.getnchannels()
            while True:
                raw = wf.readframes(chunk_samples)
                if not raw:
                    return
                yield _decode(raw, channels)
    except (wave.Error, EOFError, OSError) as exc:
        raise AudioError(f"{Path(path).name}: corrupt audio data: {exc}") from exc


def read_range(path, start: int, end: int) -> np.ndarray:
    """Read mono samples in [start, end) without loading the whole file."""
    if end <= start:
        return np.empty(0, dtype=np.float32)
    try:
        with wave.open(str(path), "rb") as wf:
            channels = wf.getnchannels()
            wf.setpos(min(start, wf.getnframes()))
            raw = wf.readframes(end - start)
    except (wave.Error, EOFError, OSError) as exc:
        raise AudioError(f"{Path(path).name}: corrupt audio data: {exc}") from exc
    return _decode(raw, channels)


@dataclass(frozen=True)
class SynthEvent:
    """One tone event for the sample generator (times in seconds)."""

    start: float
    duration: float
    freq: float = 440.0
    amp: float = 0.4
    fade_in: float = 0.0
    fade_out: float = 0.0


def synth_file(
    path,
    sample_rate: int,
    duration: float,
    events: list[SynthEvent],
    channels: int = 1,
    noise_rms: float = 0.003,
    seed: int = 0,
) -> AudioInfo:
    """Generate a 16-bit PCM wave file: low noise floor plus tone events."""
    if channels not in (1, 2):
        raise ValueError("channels must be 1 or 2")
    rng = np.random.default_rng(seed)
    n = int(round(duration * sample_rate))
    signal = rng.normal(0.0, noise_rms, n)
    for ev in events:
        i0 = int(round(ev.start * sample_rate))
        i1 = min(n, i0 + int(round(ev.duration * sample_rate)))
        if i1 <= i0:
            continue
        t = np.arange(i1 - i0) / sample_rate
        env = np.ones(i1 - i0)
        fi = min(int(round(ev.fade_in * sample_rate)), env.size)
        fo = min(int(round(ev.fade_out * sample_rate)), env.size)
        if fi:
            env[:fi] = np.arange(fi) / fi
        if fo:
            env[-fo:] = np.minimum(env[-fo:], np.arange(fo, 0, -1) / fo)
        signal[i0:i1] += ev.amp * np.sin(2.0 * np.pi * ev.freq * t) * env
    pcm = (np.clip(signal, -1.0, 1.0) * 32767).astype("<i2")
    if channels == 2:
        pcm = np.repeat(pcm[:, None], 2, axis=1)
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    with wave.open(str(path), "wb") as wf:
        wf.setnchannels(channels)
        wf.setsampwidth(SAMPWIDTH_BYTES)
        wf.setframerate(sample_rate)
        wf.writeframes(pcm.tobytes())
    return AudioInfo(path=path, sample_rate=sample_rate, channels=channels, num_samples=n)
