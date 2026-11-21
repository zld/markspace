"""RMS, peak and BS.1770-4 K-weighted gated loudness.

Measurement is streaming: the caller supplies a reader, so memory stays
bounded regardless of file length.  K-weighting is derived at the actual
sample rate from the BS.1770 analog prototypes via the bilinear transform.
"""

from __future__ import annotations

import math
from dataclasses import dataclass
from typing import Callable, Iterable

import numpy as np

Reader = Callable[[int, int], np.ndarray]

ABSOLUTE_GATE_LUFS = -70.0
RELATIVE_GATE_LU = -10.0
LOUDNESS_OFFSET = -0.691
BLOCK_HOPS = 4  # 400 ms block = four 100 ms hops

# WAV 6-channel layout assumed: FL FR FC LFE BL BR.  LFE is not measured.
CHANNEL_WEIGHTS = {
    1: (1.0,),
    2: (1.0, 1.0),
    6: (1.0, 1.0, 1.0, 0.0, 1.41, 1.41),
}


def _biquad_coeffs(kind: str, rate: int) -> tuple[list[float], list[float]]:
    """BS.1770 stage coefficients, derived at the actual sample rate.

    Uses the libebur128 parameterisation so the 48 kHz values match the
    coefficients published with the standard.
    """
    if kind == "shelf":
        freq, q, gain = 1681.974450955533, 0.7071752369554196, 3.999843853973347
        k = math.tan(math.pi * freq / rate)
        vh = 10.0 ** (gain / 20.0)
        vb = vh**0.4996667741545416
        a0 = 1.0 + k / q + k * k
        b = [
            (vh + vb * k / q + k * k) / a0,
            2.0 * (k * k - vh) / a0,
            (vh - vb * k / q + k * k) / a0,
        ]
        a = [1.0, 2.0 * (k * k - 1.0) / a0, (1.0 - k / q + k * k) / a0]
    elif kind == "highpass":
        freq, q = 38.13547087602444, 0.5003270373238773
        k = math.tan(math.pi * freq / rate)
        a0 = 1.0 + k / q + k * k
        b = [1.0, -2.0, 1.0]
        a = [1.0, 2.0 * (k * k - 1.0) / a0, (1.0 - k / q + k * k) / a0]
    else:  # pragma: no cover - internal misuse
        raise ValueError(f"unknown filter kind: {kind}")
    return b, a


def k_weighting_coefficients(rate: int) -> list[dict[str, list[float] | str]]:
    return [
        {"stage": "pre-filter (high shelf)", "b": _biquad_coeffs("shelf", rate)[0],
         "a": _biquad_coeffs("shelf", rate)[1]},
        {"stage": "RLB high-pass", "b": _biquad_coeffs("highpass", rate)[0],
         "a": _biquad_coeffs("highpass", rate)[1]},
    ]


class _Biquad:
    """Direct-form I biquad with per-channel state."""

    def __init__(self, rate: int, kind: str, channels: int) -> None:
        self.b, self.a = _biquad_coeffs(kind, rate)
        self.z1 = [0.0] * channels
        self.z2 = [0.0] * channels

    def process(self, data: np.ndarray) -> np.ndarray:
        b0, b1, b2 = self.b
        _, a1, a2 = self.a
        out = np.empty_like(data, dtype=np.float64)
        for ch in range(data.shape[1]):
            z1, z2 = self.z1[ch], self.z2[ch]
            column = data[:, ch]
            target = out[:, ch]
            for i in range(column.size):
                x = float(column[i])
                y = b0 * x + z1
                z1 = b1 * x - a1 * y + z2
                z2 = b2 * x - a2 * y
                target[i] = y
            self.z1[ch], self.z2[ch] = z1, z2
        return out


def channel_weights(channels: int) -> tuple[float, ...]:
    try:
        return CHANNEL_WEIGHTS[channels]
    except KeyError as exc:
        raise ValueError(f"unsupported channel layout: {channels}") from exc


@dataclass(frozen=True)
class Analysis:
    frames: int
    channels: int
    sample_rate: int
    rms_dbfs: float | None
    lufs: float | None
    peak_dbfs: float | None
    peak_linear: float
    clip_samples: int
    measured_blocks: int

    def as_dict(self) -> dict[str, float | int | None]:
        return {
            "frames": self.frames,
            "rms_dbfs": self.rms_dbfs,
            "lufs": self.lufs,
            "peak_dbfs": self.peak_dbfs,
            "clip_samples": self.clip_samples,
        }


def _to_db(power: float) -> float | None:
    if not math.isfinite(power) or power <= 0.0:
        return None
    return 10.0 * math.log10(power)


def _gate(blocks: list[float]) -> float | None:
    """Integrated loudness (LUFS) from 400 ms weighted mean-square blocks."""
    if not blocks:
        return None
    absolute_threshold = 10.0 ** ((ABSOLUTE_GATE_LUFS - LOUDNESS_OFFSET) / 10.0)
    gated = [z for z in blocks if z > absolute_threshold]
    if not gated:
        return None
    relative_threshold = (sum(gated) / len(gated)) * 10.0 ** (RELATIVE_GATE_LU / 10.0)
    final = [z for z in gated if z > relative_threshold]
    if not final:
        return None
    return LOUDNESS_OFFSET + 10.0 * math.log10(sum(final) / len(final))


def analyze_reader(
    reader: Reader,
    frames: int,
    channels: int,
    rate: int,
    chunk_frames: int = 65536,
) -> Analysis:
    """Measure a stream without holding it in memory."""
    frames = int(frames)
    weights = np.asarray(channel_weights(channels), dtype=np.float64)
    shelf = _Biquad(rate, "shelf", channels)
    highpass = _Biquad(rate, "highpass", channels)
    hop = max(1, int(round(rate * 0.1)))
    hop_sums = []
    recent: list[np.ndarray] = []
    blocks: list[float] = []
    total_sq = 0.0
    total_samples = 0
    peak = 0.0
    clips = 0
    pending = np.zeros(channels, dtype=np.float64)
    pending_count = 0
    offset = 0

    while offset < frames:
        count = min(chunk_frames, frames - offset)
        data = np.asarray(reader(offset, count), dtype=np.float64)
        if data.ndim == 1:
            data = data[:, None]
        if data.shape[0] != count or data.shape[1] != channels:
            raise ValueError(
                f"reader returned shape {data.shape}, expected ({count}, {channels})"
            )
        offset += count
        total_sq += float(np.square(data).sum())
        total_samples += data.size
        peak = max(peak, float(np.abs(data).max(initial=0.0)))
        clips += int(np.count_nonzero(np.abs(data) >= 1.0))
        weighted = highpass.process(shelf.process(data))
        squared = np.square(weighted)
        index = 0
        while index < count:
            take = min(hop - pending_count, count - index)
            pending += squared[index : index + take].sum(axis=0)
            pending_count += take
            index += take
            if pending_count == hop:
                hop_sums.append(pending / hop)
                recent.append(hop_sums[-1])
                if len(recent) > BLOCK_HOPS:
                    recent.pop(0)
                if len(recent) == BLOCK_HOPS:
                    mean_sq = np.mean(np.stack(recent), axis=0)
                    blocks.append(float(np.dot(weights, mean_sq)))
                pending = np.zeros(channels, dtype=np.float64)
                pending_count = 0

    rms_power = total_sq / total_samples if total_samples else 0.0
    return Analysis(
        frames=frames,
        channels=channels,
        sample_rate=rate,
        rms_dbfs=_to_db(rms_power),
        lufs=_gate(blocks),
        peak_dbfs=_to_db(peak * peak),
        peak_linear=peak,
        clip_samples=clips,
        measured_blocks=len(blocks),
    )


def analyze_array(samples: np.ndarray, rate: int, chunk_frames: int = 65536) -> Analysis:
    data = np.asarray(samples, dtype=np.float64)
    if data.ndim == 1:
        data = data[:, None]

    def reader(start: int, count: int) -> np.ndarray:
        return data[start : start + count]

    return analyze_reader(reader, data.shape[0], data.shape[1], rate, chunk_frames)


def algorithm_parameters(rate: int) -> dict[str, object]:
    """Everything needed to reproduce the measurement of a given rate."""
    return {
        "standard": "ITU-R BS.1770-4",
        "block_ms": 400,
        "hop_ms": 100,
        "absolute_gate_lufs": ABSOLUTE_GATE_LUFS,
        "relative_gate_lu": RELATIVE_GATE_LU,
        "channel_weights": {str(k): list(v) for k, v in CHANNEL_WEIGHTS.items()},
        "k_weighting": k_weighting_coefficients(rate),
    }


def gain_to_target(
    lufs: float | None, peak_linear: float, target_lufs: float, peak_ceiling_dbfs: float
) -> tuple[float, bool]:
    """Return (gain_db, loudness_normalized) respecting the peak ceiling."""
    if lufs is None or not math.isfinite(lufs):
        return 0.0, False
    ceiling = 10.0 ** (peak_ceiling_dbfs / 20.0)
    gain_db = target_lufs - lufs
    if peak_linear > 0.0 and peak_linear * 10.0 ** (gain_db / 20.0) > ceiling:
        gain_db = 20.0 * math.log10(ceiling / peak_linear)
    return gain_db, True


def rms_of(blocks: Iterable[Analysis]) -> float | None:
    """Convenience helper for combining analyses (used by tests)."""
    values = [item.rms_dbfs for item in blocks if item.rms_dbfs is not None]
    return max(values) if values else None
