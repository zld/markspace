"""Band-limited rational-ratio resampling with a polyphase windowed sinc.

The kernel is evaluated here directly; nothing is delegated to soundfile,
NumPy's sinc/interp helpers or any third-party resampler. NumPy is only used
for element-wise array arithmetic.

Positions are tracked exactly with integers::

    pos(i) = i * M / L        (input samples),  rate_out/rate_in = L/M reduced
    base(i) = (i * M) // L
    frac(i) = (i * M) % L / L

so the phase never drifts, regardless of file length.
"""

from __future__ import annotations

import math
from dataclasses import dataclass

import numpy as np

from .config import ResampleConfig
from .errors import InputError

BOUNDARY_RULE = "mirror-edge"  # -1 -> 0, -2 -> 1, N -> N-1, N+1 -> N-2
TWO_PI = 2.0 * math.pi
#: Upper bound on the (block x taps) gather buffer, in bytes. Extreme
#: downsampling ratios need very long kernels, so the working block is shrunk
#: to keep memory bounded. Block boundaries never change sample values.
GATHER_BUDGET_BYTES = 64 * 1024 * 1024


@dataclass(frozen=True)
class Ratio:
    up: int
    down: int

    @property
    def value(self) -> float:
        return self.up / self.down


def rational_ratio(rate_in: int, rate_out: int) -> Ratio:
    if rate_in <= 0 or rate_out <= 0:
        raise InputError("sample rates must be positive", reason="bad_sample_rate")
    g = math.gcd(rate_in, rate_out)
    return Ratio(up=rate_out // g, down=rate_in // g)


def resampled_frames(frames_in: int, rate_in: int, rate_out: int) -> int:
    """Output length from target duration, rounded half-up."""
    if frames_in < 0:
        raise InputError("negative frame count", reason="bad_frames")
    ratio = rational_ratio(rate_in, rate_out)
    numerator = 2 * frames_in * ratio.up
    # floor((2*N*L + M) / (2*M)) == round_half_up(N * L / M)
    return (numerator + ratio.down) // (2 * ratio.down)


def cutoff(rate_in: int, rate_out: int, rolloff: float) -> float:
    """Anti-alias cutoff relative to the input Nyquist, pulled in on downsample."""
    return rolloff * min(1.0, rate_out / rate_in)


def _sinc(x: np.ndarray) -> np.ndarray:
    """sin(pi x)/(pi x), exactly 1 at x == 0."""
    px = math.pi * x
    safe = np.where(px == 0.0, 1.0, px)
    values = np.sin(px) / safe
    return np.where(px == 0.0, 1.0, values)


def _blackman(u: np.ndarray) -> np.ndarray:
    """Blackman window on |u| <= 1 (u is in units of the kernel half-width)."""
    inside = np.abs(u) <= 1.0
    values = 0.42 + 0.5 * np.cos(math.pi * u) + 0.08 * np.cos(TWO_PI * u)
    return np.where(inside, values, 0.0)


def kernel_weights(offsets: np.ndarray, frac: np.ndarray, cut: float, zc: int) -> np.ndarray:
    """Windowed-sinc taps for every output phase in ``frac``.

    ``offsets`` are integer sample offsets from the base input index,
    ``frac`` the fractional position of each output sample. Rows are
    normalised to unity DC gain so a constant signal stays constant.
    """
    t = offsets[None, :] - frac[:, None]
    u = cut * t
    w = cut * _sinc(u) * _blackman(u / zc)
    w = np.where(np.abs(u) <= zc, w, 0.0)
    sums = w.sum(axis=1, keepdims=True)
    sums = np.where(np.abs(sums) > 1e-12, sums, 1.0)
    return w / sums


def phases(index: np.ndarray, ratio: Ratio) -> tuple[np.ndarray, np.ndarray]:
    """Integer base input index and fractional phase for each output index.

    Output sample ``i`` sits at input position ``i * down / up``; tracking it
    as an exact integer numerator avoids phase drift over long files.
    """
    pos = np.asarray(index, dtype=np.int64) * ratio.down
    base = pos // ratio.up
    frac = (pos % ratio.up).astype(np.float64) / float(ratio.up)
    return base, frac


def mirror_indices(idx: np.ndarray, frames: int) -> np.ndarray:
    """Mirror out-of-range indices; the edge sample is repeated (see rule)."""
    if frames <= 0:
        raise InputError("cannot mirror an empty signal", reason="bad_frames")
    if frames == 1:
        return np.zeros_like(idx)
    period = 2 * frames
    wrapped = np.mod(idx, period)
    return np.where(wrapped >= frames, period - 1 - wrapped, wrapped)


def resample(
    data: np.ndarray,
    rate_in: int,
    rate_out: int,
    *,
    block_size: int = 4096,
    config: ResampleConfig | None = None,
) -> np.ndarray:
    """Resample (frames, channels) float data, preserving channel order."""
    if data.ndim != 2:
        raise InputError("audio must be (frames, channels)", reason="bad_layout")
    cfg = config or ResampleConfig()
    frames_in, channels = int(data.shape[0]), int(data.shape[1])
    if block_size < 1:
        raise InputError("block_size must be >= 1", reason="bad_block_size")
    frames_out = resampled_frames(frames_in, rate_in, rate_out)
    out = np.zeros((frames_out, channels), dtype=np.float64)
    if frames_in == 0 or frames_out == 0 or channels == 0:
        return out
    if rate_in == rate_out:
        out[:] = data.astype(np.float64, copy=False)
        return out

    ratio = rational_ratio(rate_in, rate_out)
    if ratio.up == 1 and ratio.down == 1:
        out[:] = data.astype(np.float64, copy=False)
        return out
    cut = cutoff(rate_in, rate_out, cfg.rolloff)
    half = int(math.ceil(cfg.zero_crossings / cut)) + 1
    offsets = np.arange(-half, half + 1, dtype=np.int64)
    source = np.ascontiguousarray(data, dtype=np.float64)
    # Long anti-alias kernels would otherwise need a huge gather buffer.
    rows = max(1, GATHER_BUDGET_BYTES // (offsets.size * 8))
    step = max(1, min(block_size, rows))

    for start in range(0, frames_out, step):
        stop = min(start + step, frames_out)
        base, frac = phases(np.arange(start, stop, dtype=np.int64), ratio)
        weights = kernel_weights(offsets, frac, cut, cfg.zero_crossings)
        idx = base[:, None] + offsets[None, :]
        mirrored = mirror_indices(idx, frames_in)
        for channel in range(channels):
            gathered = source[mirrored, channel]
            out[start:stop, channel] = np.sum(weights * gathered, axis=1)
    return out
