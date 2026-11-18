"""Signal primitives: WAV I/O, framing, RMS energy, and synthetic test waveforms.

All time-domain boundaries exposed by this package are integer sample indices
using half-open intervals ``[start, end)``.
"""
from __future__ import annotations

import wave
from dataclasses import dataclass
from pathlib import Path

import numpy as np

DB_FLOOR = -120.0


class AudioError(ValueError):
    """Raised for unsupported, empty, corrupt or non-finite audio input."""


@dataclass(frozen=True)
class Wave:
    samples: np.ndarray  # float64, mono, finite, range roughly [-1.0, 1.0]
    sample_rate: int

    @property
    def n_samples(self) -> int:
        return int(self.samples.shape[0])


def _decode_pcm(raw: bytes, width: int, n_frames: int) -> np.ndarray:
    if width == 1:  # unsigned 8-bit PCM
        data = np.frombuffer(raw, dtype=np.uint8).astype(np.float64)
        return (data - 128.0) / 128.0
    if width == 2:
        return np.frombuffer(raw, dtype="<i2").astype(np.float64) / 32768.0
    if width == 4:
        return np.frombuffer(raw, dtype="<i4").astype(np.float64) / 2147483648.0
    if width == 3:  # signed little-endian 24-bit, no native numpy dtype
        b = np.frombuffer(raw, dtype=np.uint8).reshape(n_frames, 3)
        x = b[:, 0].astype(np.int32) | (b[:, 1].astype(np.int32) << 8) | (
            b[:, 2].astype(np.int32) << 16
        )
        x = np.where(x & 0x800000, x - 0x1000000, x)
        return x.astype(np.float64) / 8388608.0
    raise AudioError(f"unsupported sample width: {width * 8}-bit PCM")


def read_wav(path: str | Path) -> Wave:
    """Read a mono PCM WAV file, validating every structural requirement."""
    p = Path(path)
    if not p.exists() or not p.is_file():
        raise AudioError(f"file not found: {p}")
    if p.stat().st_size == 0:
        raise AudioError(f"empty file: {p}")
    try:
        with wave.open(str(p), "rb") as wf:
            n_ch, width, rate, n_frames = (
                wf.getnchannels(),
                wf.getsampwidth(),
                wf.getframerate(),
                wf.getnframes(),
            )
            comp = wf.getcomptype()
            raw = wf.readframes(n_frames)
    except (wave.Error, EOFError, OSError) as exc:
        raise AudioError(f"cannot read WAV {p}: {exc}") from exc
    if comp != "NONE":
        raise AudioError(f"unsupported compressed format {comp!r} in {p}")
    if n_ch != 1:
        raise AudioError(f"only mono PCM is supported, got {n_ch} channels: {p}")
    if width not in (1, 2, 3, 4):
        raise AudioError(f"unsupported sample width {width * 8}-bit: {p}")
    if not isinstance(rate, int) or rate <= 0:
        raise AudioError(f"invalid sample rate {rate!r}: {p}")
    if n_frames <= 0 or len(raw) < n_frames * width:
        raise AudioError(f"no audio frames in {p}")
    samples = _decode_pcm(raw[: n_frames * width], width, n_frames)
    if not np.all(np.isfinite(samples)):
        raise AudioError(f"non-finite sample values in {p}")
    return Wave(samples=samples, sample_rate=rate)


def write_wav(path: str | Path, samples: np.ndarray, sample_rate: int) -> Path:
    """Write a 16-bit mono PCM WAV file (used for generated fixtures)."""
    p = Path(path)
    p.parent.mkdir(parents=True, exist_ok=True)
    clipped = np.clip(np.asarray(samples, dtype=np.float64), -1.0, 1.0)
    pcm = np.round(clipped * 32767.0).astype("<i2")
    with wave.open(str(p), "wb") as wf:
        wf.setnchannels(1)
        wf.setsampwidth(2)
        wf.setframerate(sample_rate)
        wf.writeframes(pcm.tobytes())
    return p


def ms_to_samples(ms: float, sample_rate: int) -> int:
    return int(round(ms * sample_rate / 1000.0))


def frame_rms(samples: np.ndarray, frame_len: int, hop: int) -> tuple[np.ndarray, np.ndarray]:
    """Vectorised per-frame RMS.

    Returns ``(rms, starts)`` where ``starts[i]`` is the integer start sample
    of frame ``i``; a trailing remainder shorter than ``frame_len`` is analysed
    at its actual length instead of being dropped or zero-padded.
    """
    n = samples.shape[0]
    if frame_len <= 0 or hop <= 0:
        raise ValueError("frame_len and hop must be positive")
    starts = list(range(0, max(n - frame_len + 1, 0), hop))
    # Trailing partial frame: anything not fully covered by the stride above.
    covered = (starts[-1] + frame_len) if starts else 0
    if covered < n:
        starts.append(covered)
    if not starts:
        return np.zeros(0, dtype=np.float64), np.zeros(0, dtype=np.int64)
    idx = np.array(starts, dtype=np.int64)
    ends = np.minimum(idx + frame_len, n)
    counts = (ends - idx).astype(np.float64)
    # Sliding-window sum of squares over full frames, exact sums for the tail.
    squares = samples.astype(np.float64) ** 2
    csum = np.concatenate(([0.0], np.cumsum(squares)))
    energy = csum[ends] - csum[idx]
    rms = np.sqrt(energy / counts)
    return rms, idx


def rms_to_db(rms: np.ndarray, db_floor: float = DB_FLOOR) -> np.ndarray:
    with np.errstate(divide="ignore"):
        db = 20.0 * np.log10(np.maximum(rms, 1e-12))
    return np.maximum(db, db_floor)


def smooth_envelope(db: np.ndarray, window: int) -> np.ndarray:
    """Centered moving-average smoothing of a dB envelope; window <= 1 is a no-op."""
    if window <= 1 or db.size == 0:
        return db.copy()
    w = min(window, db.size)
    pad = w // 2
    padded = np.pad(db, (pad, w - 1 - pad), mode="edge")
    kernel = np.ones(w, dtype=np.float64) / w
    return np.convolve(padded, kernel, mode="valid")[: db.size]


def generate_waveform(
    sample_rate: int,
    duration_s: float,
    rng: np.random.Generator,
    *,
    n_voices: int,
    voice_ms: float = 620.0,
    pause_ms: float = 380.0,
    noise_rms: float = 0.004,
    voice_rms: float = 0.22,
    fade_ms: float = 35.0,
    dip: bool = False,
) -> np.ndarray:
    """Build a synthetic recording: air-conditioner floor + fading voice bursts."""
    n = int(round(duration_s * sample_rate))
    t = np.arange(n, dtype=np.float64) / sample_rate
    # Coloured-ish background noise approximating HVAC rumble.
    noise = rng.normal(0.0, noise_rms, size=n)
    if n > 4:
        noise += 0.6 * noise_rms * np.sin(2.0 * np.pi * 120.0 * t)
    sig = noise.copy()
    v_len = ms_to_samples(voice_ms, sample_rate)
    p_len = ms_to_samples(pause_ms, sample_rate)
    fade = max(1, ms_to_samples(fade_ms, sample_rate))
    cursor = ms_to_samples(220.0, sample_rate)  # leading silence
    for k in range(n_voices):
        end = min(cursor + v_len, n)
        if end <= cursor:
            break
        seg = np.arange(end - cursor, dtype=np.float64)
        env = np.ones(seg.size, dtype=np.float64)
        env[:fade] = np.linspace(0.0, 1.0, min(fade, seg.size))
        env[-fade:] = np.minimum(env[-fade:], np.linspace(1.0, 0.0, min(fade, seg.size)))
        freq = 170.0 + 45.0 * (k % 3)
        tone = np.sin(2.0 * np.pi * freq * (seg / sample_rate))
        tone += 0.35 * np.sin(2.0 * np.pi * freq * 2.01 * (seg / sample_rate))
        wobble = 1.0 + 0.12 * np.sin(2.0 * np.pi * 3.5 * (seg / sample_rate))
        burst = voice_rms * wobble * env * tone
        if dip and k == n_voices // 2 and seg.size > 8 * fade:
            # Brief low-level dip inside one utterance must not split it.
            lo, hi = seg.size // 2 - fade, seg.size // 2 + fade
            burst[lo:hi] *= 0.08
        sig[cursor:end] += burst
        cursor = end + p_len
    if not np.all(np.isfinite(sig)):
        raise AudioError("generated waveform contains non-finite samples")
    return sig.astype(np.float64)


def generate_fixtures(out_dir: str | Path, seed: int = 42, sample_rate: int = 48000) -> list[Path]:
    """Generate the three deterministic demo WAV files used by the demo flow."""
    out = Path(out_dir)
    out.mkdir(parents=True, exist_ok=True)
    rng = np.random.default_rng(seed)
    specs = [
        ("room_a.wav", 4.6, 4, False),
        ("room_b.wav", 5.4, 3, True),
        ("room_c.wav", 6.2, 4, False),
    ]
    paths: list[Path] = []
    for name, dur, voices, dip in specs:
        samples = generate_waveform(
            sample_rate, dur, rng, n_voices=voices, dip=dip
        )
        paths.append(write_wav(out / name, samples, sample_rate))
    return paths
