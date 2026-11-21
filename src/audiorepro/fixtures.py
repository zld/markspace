"""Deterministic sample-audio fixtures generated entirely in-process."""

from __future__ import annotations

from pathlib import Path

import numpy as np
import soundfile as sf

from .errors import UsageError

# (relative path, sample rate, channels, format, subtype, generator key)
FIXTURE_SPECS = (
    ("mono/mono_tone.wav", 44100, 1, "WAV", "PCM_24", "mono"),
    ("mono/mono_short.aiff", 22050, 1, "AIFF", "PCM_16", "short"),
    ("mono/mono_silent.wav", 48000, 1, "WAV", "PCM_16", "silent"),
    ("stereo/stereo_tone.flac", 48000, 2, "FLAC", "PCM_16", "stereo"),
    ("stereo/stereo_hot.wav", 44100, 2, "WAV", "FLOAT", "hot"),
    ("surround/surround_51.wav", 96000, 6, "WAV", "PCM_24", "surround"),
)


def _tone(freq: float, seconds: float, rate: int, amplitude: float, phase: float = 0.0) -> np.ndarray:
    t = np.arange(int(round(seconds * rate)), dtype=np.float64) / rate
    return amplitude * np.sin(2.0 * np.pi * freq * t + phase)


def _fade(signal: np.ndarray, rate: int, fade_ms: float = 10.0) -> np.ndarray:
    """Short raised-cosine fades so the fixtures have no click at the edges."""
    count = min(int(round(rate * fade_ms / 1000.0)), signal.shape[0] // 2)
    if count <= 1:
        return signal
    ramp = 0.5 - 0.5 * np.cos(np.linspace(0.0, np.pi, count))
    out = signal.copy()
    out[:count] *= ramp[:, None] if out.ndim == 2 else ramp
    out[-count:] *= ramp[::-1][:, None] if out.ndim == 2 else ramp[::-1]
    return out


def _stereo_pair(left: np.ndarray, right: np.ndarray) -> np.ndarray:
    return np.stack([left, right], axis=1)


def _build(key: str, rate: int, channels: int, rng: np.random.Generator) -> np.ndarray:
    if key == "mono":
        return _fade(_tone(1000.0, 2.0, rate, 10 ** (-20.0 / 20.0))[:, None], rate)
    if key == "short":
        return _fade(_tone(440.0, 0.30, rate, 10 ** (-20.0 / 20.0))[:, None], rate)
    if key == "silent":
        return np.zeros((int(round(1.5 * rate)), 1), dtype=np.float64)
    if key == "stereo":
        left = _tone(440.0, 2.0, rate, 10 ** (-21.0 / 20.0), 0.0)
        right = _tone(660.0, 2.0, rate, 10 ** (-21.0 / 20.0), 0.7)
        bed = rng.normal(0.0, 10 ** (-70.0 / 20.0), left.shape[0])
        return _fade(_stereo_pair(left + bed, right + bed), rate)
    if key == "hot":
        seconds = 1.5
        n = int(round(seconds * rate))
        signal = _tone(220.0, seconds, rate, 10 ** (-42.0 / 20.0))
        # Transient hits deliberately above full scale: these count as clipped
        # source samples but the run must peak-limit the product below 0 dBFS.
        burst = int(round(0.004 * rate))
        for start in range(0, n - burst, int(round(0.25 * rate))):
            signal[start : start + burst] += 1.4 * np.hanning(burst)
        return _fade(_stereo_pair(signal, np.roll(signal, 37))[:, :channels], rate)
    if key == "surround":
        left = _tone(440.0, 1.5, rate, 10 ** (-24.0 / 20.0))
        right = _tone(550.0, 1.5, rate, 10 ** (-24.0 / 20.0))
        centre = _tone(330.0, 1.5, rate, 10 ** (-24.0 / 20.0))
        lfe = _tone(55.0, 1.5, rate, 10 ** (-40.0 / 20.0))
        back_left = _tone(660.0, 1.5, rate, 10 ** (-27.0 / 20.0))
        back_right = _tone(770.0, 1.5, rate, 10 ** (-27.0 / 20.0))
        stacked = np.stack([left, right, centre, lfe, back_left, back_right], axis=1)
        return _fade(stacked, rate)
    raise UsageError(f"unknown fixture kind: {key}")  # pragma: no cover


def generate(out_dir: Path, seed: int = 17) -> list[Path]:
    """Write the six fixture files; returns the created paths in spec order."""
    out_dir = Path(out_dir)
    try:
        out_dir.mkdir(parents=True, exist_ok=True)
    except OSError as exc:
        raise UsageError(f"cannot create fixture directory {out_dir}: {exc}") from exc
    created: list[Path] = []
    for index, (relative, rate, channels, fmt, subtype, key) in enumerate(FIXTURE_SPECS):
        rng = np.random.default_rng(seed + index)
        samples = _build(key, rate, channels, rng)
        if samples.shape[1] != channels:  # pragma: no cover - fixture definition bug
            raise UsageError(f"fixture {relative} produced {samples.shape[1]} channels")
        path = out_dir / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        try:
            sf.write(str(path), samples, rate, format=fmt, subtype=subtype)
        except Exception as exc:
            raise UsageError(f"cannot write fixture {path}: {exc}") from exc
        created.append(path)
    return created
