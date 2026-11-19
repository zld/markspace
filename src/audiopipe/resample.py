"""Self-contained rational polyphase FIR resampler.

Design
------
For input rate ``fi`` and output rate ``fo`` the ratio is reduced with
``g = gcd(fi, fo)`` into ``fo / fi = L / M`` with coprime ``L`` and ``M``.
The interpolation kernel is a windowed sinc evaluated on the upsampled
grid of rate ``fi * L``.  Instead of materialising the zero-stuffed
signal, the kernel is split into the ``L`` polyphase branches that are
actually needed.

Filter definition (fully reproducible, reported in the JSON report):

* ``K = 32 * max(1, ceil(M / L))`` taps per phase (``K`` is always even;
  this equals 32 taps per phase at the *output* rate, independent of the
  conversion ratio),
* total prototype length ``N = K * L + 1`` taps on the upsampled grid,
* Kaiser window with shape ``beta = 8.6`` (~80 dB design stopband),
* one-sided cutoff ``fc = 0.48 * min(fi, fo)`` Hz, i.e. 96 % of the lower
  of the two Nyquist frequencies, leaving a guard band for the finite
  filter length,
* every polyphase branch is normalised to unit DC gain, so a constant
  signal is resampled to the same constant (no ripple at DC).

Because ``N`` is odd and centred at ``C = K * L / 2`` (an integer, since
``K`` is even), the output grid is aligned exactly with input time zero:
output sample ``j`` is

    y[j] = sum_{r=0..K} taps[p, r] * x[floor(j*M/L) + K/2 - r],
    p    = (j*M) mod L

so the kernel introduces **no** fractional delay.  Indices outside
``[0, n_in-1]`` read zeros.  The number of zero-padded input frames is
reported as :attr:`PolyphaseResampler.padding_frames`.

Streaming: :meth:`PolyphaseResampler.process_chunk` accepts arbitrarily
sized ``frames x channels`` float64 chunks (channels last) and returns
``frames x channels`` output.  Only enough history for the next output
sample is retained, so memory is proportional to the chunk size rather
than the file length.  Output length is ``floor(n_in * L / M)``.
Out-of-range taps are zero-filled both at the start and end of the
stream, so empty, single-frame and sub-filter-length inputs behave
deterministically.
"""

from __future__ import annotations

import math
from dataclasses import dataclass

import numpy as np

__all__ = [
    "FilterSpec",
    "RateRatio",
    "design_filter",
    "reduce_ratio",
]

TAPS_PER_PHASE = 32
KAISER_BETA = 8.6
CUTOFF_FRACTION = 0.48

# Bound the gather block so temporary index arrays stay small (~2M floats).
_MAX_BLOCK_OUTPUTS = 1 << 14
_MAX_BLOCK_FLOATS = 1 << 21


@dataclass(frozen=True)
class RateRatio:
    """Reduced conversion ratio ``out_rate / in_rate == numerator/denominator``."""

    numerator: int
    denominator: int

    @property
    def factor(self) -> float:
        return self.numerator / self.denominator

    def to_dict(self) -> dict:
        return {"numerator": self.numerator, "denominator": self.denominator}


def _check_rate(name: str, value: object) -> int:
    if not isinstance(value, int) or isinstance(value, bool) or value <= 0:
        raise ValueError(f"{name} must be a positive integer, got {value!r}")
    return value


def reduce_ratio(in_rate: int, out_rate: int) -> RateRatio:
    """Reduce ``out_rate / in_rate`` to coprime integers."""
    _check_rate("in_rate", in_rate)
    _check_rate("out_rate", out_rate)
    g = math.gcd(in_rate, out_rate)
    return RateRatio(out_rate // g, in_rate // g)


@dataclass(frozen=True)
class FilterSpec:
    """Public description of the designed kernel, suitable for the report."""

    taps_per_phase: int
    filter_length: int
    window: str
    window_beta: float
    cutoff_hz: float
    cutoff_normalized: float
    dc_gain: float

    @property
    def taps_at_input_rate(self) -> int:
        return self.filter_length

    def to_dict(self) -> dict:
        return {
            "taps_per_phase": self.taps_per_phase,
            "filter_length": self.filter_length,
            "window": self.window,
            "window_beta": self.window_beta,
            "cutoff_hz": self.cutoff_hz,
            "cutoff_normalized": self.cutoff_normalized,
            "dc_gain": self.dc_gain,
        }


def design_filter(in_rate: int, out_rate: int) -> tuple[np.ndarray, FilterSpec]:
    """Design the polyphase prototype.

    Returns ``(taps, spec)`` where ``taps`` has shape ``(L, K+1)``.  Row
    ``p`` holds the ``K+1`` coefficients used by output samples at
    interpolation phase ``p``; the final column is zero for ``p != 0``
    (it is the prototype endpoint ``i = K*L``).
    """
    _check_rate("in_rate", in_rate)
    _check_rate("out_rate", out_rate)
    ratio = reduce_ratio(in_rate, out_rate)
    L, M = ratio.numerator, ratio.denominator
    K = TAPS_PER_PHASE * max(1, math.ceil(M / L))
    N = K * L + 1
    upsampled_rate = in_rate * L
    cutoff_hz = CUTOFF_FRACTION * min(in_rate, out_rate)
    cutoff_norm = cutoff_hz / (upsampled_rate / 2.0)

    n = np.arange(N, dtype=np.float64)
    centre = (N - 1) / 2.0
    proto = np.sinc(cutoff_norm * (n - centre)) * np.kaiser(N, KAISER_BETA)
    total = float(proto.sum())
    if not np.isfinite(total) or total <= 0.0:
        raise ValueError("degenerate filter design (zero DC gain)")
    proto *= float(L) / total  # sum(proto) == L; each phase ~ unit DC

    taps = np.zeros((L, K + 1), dtype=np.float64)
    for phase in range(L):
        row = proto[phase::L]
        taps[phase, : row.size] = row[: K + 1]
    sums = taps.sum(axis=1)
    if not np.all(np.isfinite(sums)) or np.any(np.abs(sums) < 1e-12):
        raise ValueError("degenerate filter design (zero-gain phase)")
    taps /= sums[:, None]

    spec = FilterSpec(
        taps_per_phase=K,
        filter_length=N,
        window="kaiser",
        window_beta=KAISER_BETA,
        cutoff_hz=round(float(cutoff_hz), 6),
        cutoff_normalized=round(float(cutoff_norm), 9),
        dc_gain=1.0,
    )
    return taps, spec


def _identity_spec(in_rate: int, out_rate: int) -> FilterSpec:
    return FilterSpec(
        taps_per_phase=0,
        filter_length=0,
        window="none",
        window_beta=0.0,
        cutoff_hz=float(min(in_rate, out_rate)) / 2.0,
        cutoff_normalized=1.0,
        dc_gain=1.0,
    )
