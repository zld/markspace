"""Polyphase windowed-sinc resampling with a shared coefficient cache.

The kernel is a Blackman-windowed sinc whose cutoff follows the lower of the
two Nyquist frequencies, so downsampling is band-limited.  Coefficients are
tabulated per fractional phase and cached by the (input, output) rate pair;
a batch of files therefore designs each kernel only once.
"""

from __future__ import annotations

import math
import threading
from dataclasses import asdict, dataclass
from typing import Callable, Iterator

import numpy as np

Reader = Callable[[int, int], np.ndarray]


@dataclass(frozen=True)
class ResamplerConfig:
    half_width: int = 128
    phases: int = 2048
    cutoff_ratio: float = 0.95

    def as_dict(self) -> dict[str, float | int | str]:
        return {
            "kernel": "blackman-windowed-sinc",
            "half_width": self.half_width,
            "phases": self.phases,
            "cutoff_ratio": self.cutoff_ratio,
        }


@dataclass(frozen=True)
class CoeffTable:
    in_rate: int
    out_rate: int
    step: float
    taps: int
    half_width: int
    phases: int
    coeffs: np.ndarray

    def as_dict(self) -> dict[str, float | int]:
        data = asdict(self)
        data.pop("coeffs")
        return data


class CoefficientCache:
    """Thread-safe LRU cache of polyphase kernels."""

    def __init__(self, config: ResamplerConfig | None = None, max_entries: int = 8) -> None:
        self.config = config or ResamplerConfig()
        self.max_entries = max_entries
        self._tables: dict[tuple[int, int], CoeffTable] = {}
        self._hits = 0
        self._misses = 0
        self._lock = threading.Lock()

    def stats(self) -> dict[str, int]:
        with self._lock:
            return {
                "hits": self._hits,
                "misses": self._misses,
                "entries": len(self._tables),
            }

    def get(self, in_rate: int, out_rate: int) -> CoeffTable:
        key = (int(in_rate), int(out_rate))
        with self._lock:
            table = self._tables.get(key)
            if table is not None:
                self._tables[key] = self._tables.pop(key)
                self._hits += 1
                return table
            self._misses += 1
            if len(self._tables) >= self.max_entries:
                self._tables.pop(next(iter(self._tables)))
            table = self._design(key[0], key[1])
            self._tables[key] = table
            return table

    def _design(self, in_rate: int, out_rate: int) -> CoeffTable:
        cfg = self.config
        half = cfg.half_width
        taps = 2 * half
        cutoff = cfg.cutoff_ratio * min(1.0, out_rate / in_rate)
        offsets = np.arange(cfg.phases, dtype=np.float64) / cfg.phases
        # t is the distance from the output instant to each kernel tap.
        t = offsets[:, None] + (half - 1) - np.arange(taps, dtype=np.float64)[None, :]
        window = np.where(
            np.abs(t) < half,
            0.42 + 0.5 * np.cos(np.pi * t / half) + 0.08 * np.cos(2.0 * np.pi * t / half),
            0.0,
        )
        coeffs = cutoff * np.sinc(cutoff * t) * window
        sums = coeffs.sum(axis=1, keepdims=True)
        coeffs = np.divide(coeffs, sums, out=np.zeros_like(coeffs), where=sums != 0.0)
        return CoeffTable(
            in_rate=in_rate,
            out_rate=out_rate,
            step=in_rate / out_rate,
            taps=taps,
            half_width=half,
            phases=cfg.phases,
            coeffs=coeffs,
        )


def output_length(in_len: int, in_rate: int, out_rate: int) -> int:
    """Output frame count, rounded up as required by the contract."""
    return max(0, (int(in_len) * int(out_rate) + int(in_rate) - 1) // int(in_rate))


def _read_padded(reader: Reader, start: int, count: int, in_len: int, channels: int) -> np.ndarray:
    """Read [start, start+count) from the source, zero-padding the edges."""
    seg = np.zeros((count, channels), dtype=np.float64)
    read_start = max(0, start)
    read_end = min(in_len, start + count)
    if read_end > read_start:
        data = np.asarray(reader(read_start, read_end - read_start), dtype=np.float64)
        if data.ndim == 1:
            data = data[:, None]
        if data.shape[1] != channels:
            raise ValueError(f"reader returned {data.shape[1]} channels, expected {channels}")
        seg[read_start - start : read_end - start] = data
    return seg


class Resampler:
    def __init__(self, cache: CoefficientCache | None = None) -> None:
        self.cache = cache or CoefficientCache()

    def resample_reader(
        self,
        reader: Reader,
        in_len: int,
        channels: int,
        in_rate: int,
        out_rate: int,
        chunk_frames: int = 16384,
    ) -> Iterator[np.ndarray]:
        """Yield output blocks of shape (frames, channels) using bounded memory."""
        table = self.cache.get(in_rate, out_rate)
        out_len = output_length(in_len, in_rate, out_rate)
        taps = table.taps
        half = table.half_width
        phases = table.phases
        tap_idx = np.arange(taps, dtype=np.int64)
        for out_start in range(0, out_len, chunk_frames):
            count = min(chunk_frames, out_len - out_start)
            pos = (out_start + np.arange(count, dtype=np.float64)) * table.step
            base = np.floor(pos).astype(np.int64) - half + 1
            frac = pos - np.floor(pos)
            phase_idx = np.rint(frac * phases).astype(np.int64) % phases
            in_start = int(base[0])
            in_count = int(base[-1] + taps - in_start)
            seg = _read_padded(reader, in_start, in_count, in_len, channels)
            rel = base[:, None] + tap_idx[None, :] - in_start
            out = np.empty((count, channels), dtype=np.float64)
            for ch in range(channels):
                gathered = seg[rel, ch] * table.coeffs[phase_idx]
                out[:, ch] = gathered.sum(axis=1)
            yield out

    def resample_array(
        self, samples: np.ndarray, in_rate: int, out_rate: int, chunk_frames: int = 16384
    ) -> np.ndarray:
        data = np.asarray(samples, dtype=np.float64)
        if data.ndim == 1:
            data = data[:, None]
        channels = data.shape[1]

        def reader(start: int, count: int) -> np.ndarray:
            return data[start : start + count]

        blocks = list(
            self.resample_reader(reader, data.shape[0], channels, in_rate, out_rate, chunk_frames)
        )
        return np.concatenate(blocks, axis=0) if blocks else np.zeros((0, channels))


def suggest_cutoff_hz(in_rate: int, out_rate: int, config: ResamplerConfig | None = None) -> float:
    cfg = config or ResamplerConfig()
    return 0.5 * min(in_rate, out_rate) * cfg.cutoff_ratio


def is_supported_rate(rate: int) -> bool:
    return 8000 <= int(rate) <= 192000


def channels_label(channels: int) -> str:
    return {1: "mono", 2: "stereo", 6: "5.1"}.get(channels, f"{channels}ch")


def gcd_ratio(in_rate: int, out_rate: int) -> tuple[int, int]:
    g = math.gcd(in_rate, out_rate) or 1
    return in_rate // g, out_rate // g
