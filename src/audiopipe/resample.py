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
    "PolyphaseResampler",
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


class PolyphaseResampler:
    """Streaming multi-channel rational resampler."""

    def __init__(self, in_rate: int, out_rate: int, channels: int) -> None:
        _check_rate("in_rate", in_rate)
        _check_rate("out_rate", out_rate)
        if not isinstance(channels, int) or isinstance(channels, bool) or not 1 <= channels <= 8:
            raise ValueError(f"channels must be an integer between 1 and 8, got {channels!r}")
        self.in_rate = in_rate
        self.out_rate = out_rate
        self.channels = channels
        self.ratio = reduce_ratio(in_rate, out_rate)
        self.identity = self.ratio.numerator == self.ratio.denominator
        if self.identity:
            self.taps = np.zeros((1, 0), dtype=np.float64)
            self.spec = _identity_spec(in_rate, out_rate)
        else:
            if self.ratio.numerator > 4096 or self.ratio.denominator > 4096:
                raise ValueError(
                    "sample rate ratio is too large to design a practical kernel: "
                    f"{self.ratio.numerator}/{self.ratio.denominator}"
                )
            self.taps, self.spec = design_filter(in_rate, out_rate)

        self._K = self.spec.taps_per_phase
        self._half = self._K // 2
        self._buf = np.zeros((0, channels), dtype=np.float64)
        self._buf_start = 0
        self._n_received = 0
        self._next_output = 0
        self._finished = False
        self._total_output = 0

    # -- geometry ----------------------------------------------------------
    @property
    def _L(self) -> int:
        return self.ratio.numerator

    @property
    def _M(self) -> int:
        return self.ratio.denominator

    def output_frames_for(self, input_frames: int) -> int:
        if input_frames < 0:
            raise ValueError("input_frames must be non-negative")
        if self.identity:
            return input_frames
        return (input_frames * self._L) // self._M

    @property
    def padding_frames(self) -> int:
        """Zero-padded input frames required for the convolution.

        Counts the virtual pre-roll of ``K/2`` frames on the left and the
        trailing taps that cross the end of the signal on the right.  An
        empty input needs no padding because no output sample exists.
        """
        return self.padding_breakdown[0] + self.padding_breakdown[1]

    @property
    def padding_breakdown(self) -> tuple[int, int]:
        if self.identity or self._n_received == 0:
            return (0, 0)
        n_out = self.output_frames_for(self._n_received)
        if n_out == 0:
            return (self._half, 0)
        last_j = n_out - 1
        last_needed = (last_j * self._M) // self._L + self._half
        right = max(0, last_needed - (self._n_received - 1))
        return (self._half, right)

    # -- processing --------------------------------------------------------
    def process_chunk(self, chunk: np.ndarray) -> np.ndarray:
        """Resample one chunk and return ``(out_frames, channels)`` float64."""
        if self._finished:
            raise RuntimeError("process_chunk() called after finish()")
        arr = np.asarray(chunk, dtype=np.float64)
        if arr.ndim != 2 or arr.shape[1] != self.channels:
            raise ValueError(
                f"expected a 2-D array with {self.channels} channels, got shape {arr.shape}"
            )
        if not np.all(np.isfinite(arr)):
            raise ValueError("input contains non-finite samples")
        if arr.shape[0] == 0:
            return np.zeros((0, self.channels), dtype=np.float64)
        self._n_received += arr.shape[0]
        if self.identity:
            return arr.copy()

        self._buf = np.concatenate((self._buf, arr), axis=0)
        available_last = self._n_received - 1
        limit = available_last - self._half
        if limit < 0:
            return np.zeros((0, self.channels), dtype=np.float64)
        # Smallest j_stop with floor(j*M/L) > limit, i.e. ceil((limit+1)*L/M).
        j_stop = ((limit + 1) * self._L + self._M - 1) // self._M
        out = self._emit(self._next_output, j_stop, buffered=True)
        self._next_output += out.shape[0]
        self._prune()
        return out

    def finish(self) -> np.ndarray:
        """Emit the remaining output frames (tail zero-padded)."""
        if self._finished:
            raise RuntimeError("finish() called twice")
        self._finished = True
        if self.identity:
            self._total_output = self._n_received
            return np.zeros((0, self.channels), dtype=np.float64)
        total = self.output_frames_for(self._n_received)
        out = self._emit(self._next_output, total, buffered=False)
        self._total_output = total
        self._next_output = total
        return out

    def _prune(self) -> None:
        if self.identity:
            return
        # Output j reads input indices floor(j*M/L) + K/2 - K ... + K/2.
        keep_from = (self._next_output * self._M) // self._L - self._half
        keep_from = max(0, keep_from)
        drop = keep_from - self._buf_start
        if drop > 0:
            self._buf = self._buf[drop:]
            self._buf_start = keep_from

    def _emit(self, j_start: int, j_stop: int, *, buffered: bool) -> np.ndarray:
        if j_stop <= j_start:
            return np.zeros((0, self.channels), dtype=np.float64)
        if self.identity:
            raise RuntimeError("identity resampler has no filtered path")
        count = j_stop - j_start
        out = np.empty((count, self.channels), dtype=np.float64)
        K = self._K
        half = self._half
        taps = self.taps
        r = np.arange(K + 1, dtype=np.int64)
        block = max(1, min(_MAX_BLOCK_OUTPUTS, _MAX_BLOCK_FLOATS // max(1, (K + 1) * self.channels)))
        buf_len = self._buf.shape[0]
        for off in range(0, count, block):
            stop = min(count, off + block)
            js = np.arange(j_start + off, j_start + stop, dtype=np.int64)
            q = (js * self._M) // self._L
            phases = (js * self._M) % self._L
            idx = q[:, None] + half - r[None, :]  # absolute input index
            rel = idx - self._buf_start
            buf_end = self._buf_start + buf_len  # exclusive
            valid = (idx >= 0) & (idx < buf_end)  # zeros outside the signal
            if buffered:
                # Mid-stream the buffer must cover every non-negative index;
                # only the virtual pre-roll (idx < 0 at the stream start) is
                # allowed to fall outside it.
                floor = 0 if self._buf_start == 0 else self._buf_start
                if not np.all((idx < 0) | ((idx >= floor) & (idx < buf_end))):
                    raise RuntimeError(
                        "resampler buffer underrun; chunk history was pruned too aggressively"
                    )
            np.clip(rel, 0, buf_len - 1, out=rel)
            weights = taps[phases]
            for ch in range(self.channels):
                gathered = self._buf[rel, ch]
                if not np.all(valid):
                    gathered = np.where(valid, gathered, 0.0)
                out[off:stop, ch] = np.einsum(
                    "ij,ij->i", weights, gathered, optimize=False
                )
        if not np.all(np.isfinite(out)):
            raise ValueError("resampler produced non-finite output")
        return out

    @property
    def input_frames(self) -> int:
        return self._n_received

    @property
    def output_frames(self) -> int:
        return self._total_output if self._finished else self._next_output

    @property
    def expected_output_frames(self) -> int:
        return self.output_frames_for(self._n_received)


def resample_array(
    data: np.ndarray, in_rate: int, out_rate: int, *, block_frames: int = 65536
) -> tuple[np.ndarray, PolyphaseResampler]:
    """Convenience wrapper: resample a whole ``frames x channels`` array."""
    arr = np.asarray(data, dtype=np.float64)
    if arr.ndim != 2:
        raise ValueError("expected a 2-D frames x channels array")
    rs = PolyphaseResampler(in_rate, out_rate, arr.shape[1])
    parts = []
    for start in range(0, arr.shape[0], max(1, block_frames)):
        parts.append(rs.process_chunk(arr[start : start + block_frames]))
    parts.append(rs.finish())
    if not parts:
        return np.zeros((0, arr.shape[1])), rs
    out = np.concatenate(parts, axis=0) if len(parts) > 1 else parts[0]
    return out, rs
