"""Synthetic audio fixture generation (no external audio files).

All material is deterministic and synthesized with NumPy so the demo and
test suites are fully self-contained.  Fixtures cover every supported
channel layout, both up- and down-sampling cases, an explicit silence
file (null LUFS path) and a peak-limiting edge case.
"""

from __future__ import annotations

from pathlib import Path

import numpy as np
import soundfile as sf

#: (relative path, sample rate, channel count, seconds, builder name).
FIXTURE_SPECS: tuple[tuple[str, int, int, float, str], ...] = (
    ("mono_44k.wav", 44100, 1, 3.0, "speech_like"),
    ("stereo_44k.wav", 44100, 2, 3.0, "music_like"),
    ("surround_48k_5_1.flac", 48000, 6, 3.0, "ambience_5_1"),
    ("stereo_96k_downsample.wav", 96000, 2, 2.5, "bright_music"),
    ("mono_22k_upsample.wav", 22050, 1, 2.5, "speech_like"),
    ("silence_stereo.wav", 48000, 2, 1.5, "silence"),
    ("peak_limited_edge.wav", 48000, 1, 2.0, "spiky"),
)


def _time_axis(seconds: float, rate: int) -> np.ndarray:
    return np.arange(int(round(seconds * rate)), dtype=np.float64) / rate


def _speech_like(seconds: float, rate: int, rng: np.random.Generator
                 ) -> np.ndarray:
    t = _time_axis(seconds, rate)
    sig = (0.55 * np.sin(2 * np.pi * 220 * t)
           + 0.30 * np.sin(2 * np.pi * 440 * t)
           + 0.12 * np.sin(2 * np.pi * 880 * t))
    envelope = 0.6 + 0.4 * np.sin(2 * np.pi * 2.5 * t)
    sig = sig * envelope
    sig += 0.02 * rng.standard_normal(t.shape)
    return sig


def _music_like(seconds: float, rate: int, rng: np.random.Generator
                ) -> np.ndarray:
    t = _time_axis(seconds, rate)
    left = (0.5 * np.sin(2 * np.pi * 330 * t)
            + 0.25 * np.sin(2 * np.pi * 660 * t)
            + 0.15 * np.sin(2 * np.pi * 1320 * t))
    right = (0.5 * np.sin(2 * np.pi * 392 * t)
             + 0.25 * np.sin(2 * np.pi * 784 * t)
             + 0.15 * np.sin(2 * np.pi * 1568 * t))
    fade = np.minimum(1.0, t / 0.05) * np.minimum(
        1.0, (seconds - t) / 0.05)
    sig = np.stack([left, right], axis=1) * fade[:, None]
    sig += 0.015 * rng.standard_normal(sig.shape)
    return sig


def _ambience_5_1(seconds: float, rate: int, rng: np.random.Generator
                  ) -> np.ndarray:
    t = _time_axis(seconds, rate)
    base = np.sin(2 * np.pi * 180 * t)
    sig = np.zeros((t.shape[0], 6), dtype=np.float64)
    sig[:, 0] = 0.45 * base                                  # L
    sig[:, 1] = 0.45 * np.sin(2 * np.pi * 220 * t)          # R
    sig[:, 2] = 0.35 * np.sin(2 * np.pi * 140 * t)          # C
    sig[:, 3] = 0.40 * np.sin(2 * np.pi * 60 * t)           # LFE
    sig[:, 4] = 0.30 * np.sin(2 * np.pi * 500 * t)          # Ls
    sig[:, 5] = 0.30 * np.sin(2 * np.pi * 540 * t)          # Rs
    sig += 0.01 * rng.standard_normal(sig.shape)
    return sig


def _bright_music(seconds: float, rate: int, rng: np.random.Generator
                  ) -> np.ndarray:
    t = _time_axis(seconds, rate)
    left = sum(0.3 / k * np.sin(2 * np.pi * (440 * k) * t)
               for k in range(1, 9))
    right = sum(0.3 / k * np.sin(2 * np.pi * (494 * k) * t)
                for k in range(1, 9))
    sig = np.stack([left, right], axis=1)
    sig += 0.01 * rng.standard_normal(sig.shape)
    return sig


def _silence(seconds: float, rate: int, rng: np.random.Generator
             ) -> np.ndarray:
    return np.zeros((int(round(seconds * rate)), 2), dtype=np.float64)


def _spiky(seconds: float, rate: int, rng: np.random.Generator
           ) -> np.ndarray:
    """Moderate bed with sparse near-full-scale impulses.

    Normalizing this to a louder target must be peak-limited rather than
    clipping, exercising the constrained-gain reporting path.
    """
    t = _time_axis(seconds, rate)
    sig = 0.12 * np.sin(2 * np.pi * 300 * t)
    spike_times = np.arange(0.25, seconds, 0.4)
    for st in spike_times:
        center = int(round(st * rate))
        width = 24
        idx = np.arange(-width, width + 1)
        sig[center + idx] += 0.95 * np.sinc(idx / 6.0)
    sig += 0.005 * rng.standard_normal(t.shape)
    return np.clip(sig, -0.999, 0.999)


_BUILDERS = {
    "speech_like": _speech_like,
    "music_like": _music_like,
    "ambience_5_1": _ambience_5_1,
    "bright_music": _bright_music,
    "silence": _silence,
    "spiky": _spiky,
}


def _normalize_peak(sig: np.ndarray, target_peak: float = 0.7) -> np.ndarray:
    peak = float(np.max(np.abs(sig))) if sig.size else 0.0
    if peak > 0:
        sig = sig * (target_peak / peak)
    return sig


def generate_fixtures(directory: Path, seed: int = 14630
                      ) -> list[Path]:
    """Synthesize every fixture under ``directory``; return file paths."""
    directory = Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    rng = np.random.default_rng(seed)
    written: list[Path] = []
    for rel, rate, channels, seconds, builder in FIXTURE_SPECS:
        sig = _BUILDERS[builder](seconds, rate, rng)
        if sig.ndim == 1:
            sig = sig[:, None]
        # Silence stays exact zero; the spiky edge case keeps its raw
        # near-full-scale impulses so peak limiting is actually exercised.
        if builder not in ("silence", "spiky"):
            sig = _normalize_peak(sig)
        if builder == "spiky":
            sig = np.clip(sig, -0.999, 0.999)
        if not np.all(np.isfinite(sig)):
            raise RuntimeError(f"non-finite samples in fixture {rel}")
        out = directory / rel
        out.parent.mkdir(parents=True, exist_ok=True)
        sf.write(str(out), sig.astype(np.float64), rate)
        written.append(out)
    return written
