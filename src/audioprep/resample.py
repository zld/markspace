"""Self-developed polyphase FIR resampling kernel.

No third-party resampler is used. The ratio is reduced to up/down integers,
a windowed-sinc lowpass FIR is designed in the upsampled domain, decomposed
into polyphase branches (one per phase accumulator residue), and applied with
zero-extension at the boundaries. Batch processing reuses ResamplePlan objects
so the filter is designed once per (in_rate, out_rate) pair.
"""
from __future__ import annotations

import math
from dataclasses import dataclass

import numpy as np

TAPS_PER_PHASE = 256   # target taps per polyphase branch
KAISER_BETA = 9.0      # stopband attenuation ~ -70 dB
CUTOFF_MARGIN = 0.95   # cutoff at 95% of the lower Nyquist
_CHUNK = 4096          # outputs per vectorized gather block


def reduce_ratio(in_rate: int, out_rate: int) -> tuple[int, int]:
    """Reduce sample rates to coprime (up, down) integers."""
    for name, rate in (("in_rate", in_rate), ("out_rate", out_rate)):
        if not isinstance(rate, (int, np.integer)) or int(rate) <= 0:
            raise ValueError(f"{name} must be a positive integer, got {rate!r}")
    g = math.gcd(int(in_rate), int(out_rate))
    return int(out_rate) // g, int(in_rate) // g


def output_frame_count(n_in: int, in_rate: int, out_rate: int) -> int:
    """Exact output frame count: ceil(n_in * up / down)."""
    up, down = reduce_ratio(in_rate, out_rate)
    return -(-int(n_in) * up // down)


def design_lowpass(up: int, down: int,
                   taps_per_phase: int = TAPS_PER_PHASE,
                   beta: float = KAISER_BETA) -> np.ndarray:
    """Windowed-sinc lowpass as polyphase matrix, shape (up, taps).

    The cutoff sits at CUTOFF_MARGIN * min(1/up, 1/down) / 2 cycles per
    upsampled sample, so both the interpolation images and the decimation
    aliases are suppressed. Each branch is normalized to unit DC gain, which
    removes per-phase amplitude ripple for slowly varying signals.
    """
    length = taps_per_phase * max(up, down)
    length = int(math.ceil(length / up)) * up
    n = np.arange(length, dtype=np.float64) - length // 2
    fc = 0.5 * min(1.0 / up, 1.0 / down) * CUTOFF_MARGIN
    h = 2.0 * fc * np.sinc(2.0 * fc * n)
    h *= np.kaiser(length, beta)
    phases = h.reshape(-1, up).T.copy()  # phases[p, j] = h[j * up + p]
    phases /= phases.sum(axis=1, keepdims=True)
    return phases


@dataclass(frozen=True)
class ResamplePlan:
    """Reusable resampling plan for one (in_rate, out_rate) pair."""
    in_rate: int
    out_rate: int
    up: int
    down: int
    phases: np.ndarray  # (up, taps) polyphase branches
    center: int         # filter center index in the upsampled domain

    @property
    def taps(self) -> int:
        return int(self.phases.shape[1])


def plan_resample(in_rate: int, out_rate: int,
                  taps_per_phase: int = TAPS_PER_PHASE,
                  beta: float = KAISER_BETA) -> ResamplePlan:
    up, down = reduce_ratio(in_rate, out_rate)
    phases = design_lowpass(up, down, taps_per_phase, beta)
    center = (phases.shape[1] * up) // 2
    return ResamplePlan(int(in_rate), int(out_rate), up, down, phases, center)


def apply_plan(plan: ResamplePlan, x: np.ndarray) -> np.ndarray:
    """Resample a mono 1-D signal. Identity shortcut for equal rates.

    Output m uses phase p = (m*down + center) % up and base q = div(up):
    y[m] = sum_j x[q - j] * phases[p, j], with x zero-extended outside
    [0, n_in). Evaluation is chunked to bound the gather-matrix memory.
    """
    x = np.asarray(x, dtype=np.float64)
    n_in = x.shape[0]
    if plan.up == 1 and plan.down == 1:
        return x.copy()
    n_out = -(-n_in * plan.up // plan.down)
    y = np.empty(n_out, dtype=np.float64)
    if n_in == 0:
        return y
    t = np.arange(n_out, dtype=np.int64) * plan.down + plan.center
    q = t // plan.up
    p = t % plan.up
    taps = plan.taps
    j_off = np.arange(taps, dtype=np.int64)
    for phase in range(plan.up):
        out_idx = np.nonzero(p == phase)[0]
        if out_idx.size == 0:
            continue
        coeff = plan.phases[phase]
        qs = q[out_idx]
        for s in range(0, qs.size, _CHUNK):
            idx = qs[s:s + _CHUNK, None] - j_off[None, :]
            valid = (idx >= 0) & (idx < n_in)
            vals = x[np.clip(idx, 0, n_in - 1)] * valid
            y[out_idx[s:s + _CHUNK]] = vals @ coeff
    return y


def resample(x: np.ndarray, in_rate: int, out_rate: int, **kw) -> np.ndarray:
    """One-shot convenience wrapper around plan_resample/apply_plan."""
    return apply_plan(plan_resample(in_rate, out_rate, **kw), x)


def impulse_response(plan: ResamplePlan) -> tuple[np.ndarray, int]:
    """Reconstruct the full centered FIR (upsampled domain) and its center.

    Used by tests and verification to evaluate the frequency response.
    """
    h = plan.phases.T.reshape(-1)  # h[j * up + p] = phases[p, j]
    return h, plan.center
