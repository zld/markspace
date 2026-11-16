"""From-scratch polyphase windowed-sinc resampler.

No third-party resampling library is used. A single rational ratio
``L/D = out_rate/in_rate`` (reduced by the greatest common divisor) drives the
filter. For output sample ``j`` the fractional input position is
``c = j*D/L``; writing ``p = (j*D) mod L`` the fractional part is ``p/L``.
Taps are built directly on the input-sample grid:

* beta = min(1, L/D) sets the anti-alias/anti-image cutoff as a fraction of
  the input Nyquist band (downsampling: beta = L/D < 1; upsampling: 1.0);
* a Hann-windowed sinc ``beta*sinc(beta*t)*hann(t)`` with ``HALF_TAPS`` zero
  crossings per side gives exactly ``2*HALF_TAPS`` taps for each of the
  ``L`` phases, so the kernel shape is identical in both directions;
* each phase row is renormalized to unit DC gain.

A :class:`ResamplerPlan` is immutable and safe to share read-only across
worker threads. Per-file phase/buffer state lives in
:class:`StreamingResampler`, so each file's phase state is independent.
"""

from __future__ import annotations

import math
from dataclasses import dataclass
from fractions import Fraction

import numpy as np

SUPPORTED_RATE_MIN = 8_000
SUPPORTED_RATE_MAX = 192_000
# Zero crossings of the prototype on each side (high-rate grid).
HALF_TAPS = 16


class ResampleError(ValueError):
    """Raised for unsupported sample rates or invalid plan construction."""


def _sinc(normalized: np.ndarray) -> np.ndarray:
    """Unit sinc: sin(pi*x)/(pi*x), with sinc(0) = 1."""
    out = np.empty_like(normalized, dtype=np.float64)
    zero = normalized == 0.0
    out[zero] = 1.0
    nz = ~zero
    out[nz] = np.sin(np.pi * normalized[nz]) / (np.pi * normalized[nz])
    return out


def output_frame_count(input_frames: int, in_rate: int, out_rate: int) -> int:
    """Nominal output length: ceil(N * out_rate / in_rate)."""
    if input_frames <= 0:
        return 0
    return math.ceil(input_frames * out_rate / in_rate)


@dataclass(frozen=True)
class ResamplerPlan:
    """Immutable, thread-safe polyphase coefficient plan."""

    in_rate: int
    out_rate: int
    up: int  # L
    down: int  # D
    cutoff_beta: float  # min(1, L/D): cutoff as fraction of input Nyquist
    # Shape (L, 2*HALF_TAPS); row p are the taps for fractional phase p/L.
    phases: np.ndarray
    passthrough: bool

    @property
    def half_taps(self) -> int:
        return HALF_TAPS

    def expected_frames(self, input_frames: int) -> int:
        return output_frame_count(input_frames, self.in_rate, self.out_rate)


def _validate_rate(rate: int, label: str) -> None:
    if not isinstance(rate, int) or not (
        SUPPORTED_RATE_MIN <= rate <= SUPPORTED_RATE_MAX
    ):
        raise ResampleError(
            f"{label} sample rate {rate!r} outside supported range "
            f"{SUPPORTED_RATE_MIN}..{SUPPORTED_RATE_MAX} Hz"
        )


def build_plan(in_rate: int, out_rate: int) -> ResamplerPlan:
    """Construct the shared coefficient plan for a rate pair."""
    _validate_rate(in_rate, "input")
    _validate_rate(out_rate, "output")
    if in_rate == out_rate:
        passthrough_phases = np.empty((1, 2 * HALF_TAPS), dtype=np.float64)
        passthrough_phases.setflags(write=False)
        return ResamplerPlan(
            in_rate=in_rate,
            out_rate=out_rate,
            up=1,
            down=1,
            cutoff_beta=1.0,
            phases=passthrough_phases,
            passthrough=True,
        )

    ratio = Fraction(out_rate, in_rate)
    up, down = ratio.numerator, ratio.denominator
    # Fraction of the input Nyquist band retained by the low-pass filter.
    beta = min(1.0, up / down)
    radius = HALF_TAPS

    phases = np.zeros((up, 2 * HALF_TAPS), dtype=np.float64)
    tap_idx = np.arange(-HALF_TAPS + 1, HALF_TAPS + 1, dtype=np.float64)
    window = 0.5 * (1.0 + np.cos(np.pi * tap_idx / radius))
    for p in range(up):
        frac = p / up
        t = tap_idx - frac
        taps = beta * _sinc(beta * t) * window
        # Normalize DC gain per phase to exactly 1.0 (windowing error removed).
        phases[p] = taps / taps.sum()
    # The plan is shared read-only across worker threads; make mutation raise.
    phases.setflags(write=False)

    return ResamplerPlan(
        in_rate=in_rate,
        out_rate=out_rate,
        up=up,
        down=down,
        cutoff_beta=beta,
        phases=phases,
        passthrough=False,
    )


class StreamingResampler:
    """Frame-blocked resampler with per-file independent phase state.

    Input is fed via :meth:`process_block` as ``(frames, channels)`` arrays.
    Only outputs whose full kernel footprint lies inside the received input
    are emitted; :meth:`flush` treats the missing tail as zero padding (which
    participates in convolution but never in duration/loudness accounting)
    and trims the result to the nominal
    ``ceil(N * out_rate / in_rate)`` length.
    """

    def __init__(self, plan: ResamplerPlan):
        self.plan = plan
        self._buffer = np.empty((0, 0), dtype=np.float64)
        self._offset = 0  # input-frame index of buffer row zero
        self._total_in = 0
        self._n_out = 0

    def _append(self, block: np.ndarray) -> None:
        block = np.asarray(block, dtype=np.float64)
        if block.ndim == 1:
            block = block[:, None]
        if self._buffer.size == 0:
            self._buffer = block.copy()
        else:
            if block.shape[1] != self._buffer.shape[1]:
                raise ResampleError("channel count changed mid-stream")
            self._buffer = np.concatenate((self._buffer, block), axis=0)
        self._total_in += block.shape[0]

    def _next_geometry(self) -> tuple[int, int, int]:
        """Return (phase row, first tap input index, output index j).

        Output j sits at fractional input position j*D/L = center + p/L.
        """
        j = self._n_out
        t = j * self.plan.down
        center = t // self.plan.up
        phase = t % self.plan.up
        start = center - HALF_TAPS + 1
        return phase, start, j

    def _gather(self, j_indices: np.ndarray, tail_pad: bool) -> np.ndarray:
        """Vector polyphase convolution for the given absolute output indices.

        The current buffer covers global input frames ``[offset, offset +
        len(buffer))``. It is wrapped in leading zeros (and, at flush,
        trailing zeros); any tap index outside the real input range reads as
        zero. Returns shape ``(len(j_indices), channels)``.
        """
        up, down = self.plan.up, self.plan.down
        t = j_indices * down
        centers = t // up
        phases = t % up
        starts = centers - HALF_TAPS + 1
        tap_offsets = np.arange(2 * HALF_TAPS)
        source_index = starts[:, None] + tap_offsets[None, :]  # (J, K)
        in_range = (source_index >= 0) & (source_index < self._total_in)

        pad = HALF_TAPS - 1
        trailing = pad if tail_pad else 0
        padded = np.concatenate(
            (
                np.zeros((pad, self._buffer.shape[1])),
                self._buffer,
                np.zeros((trailing, self._buffer.shape[1])),
            ),
            axis=0,
        )
        # Local row inside `padded` for a global input index.
        local_row = source_index - self._offset + pad
        safe_row = np.where(in_range, local_row, 0)
        samples = padded[safe_row]  # (J, K, channels)
        samples = np.where(in_range[:, :, None], samples, 0.0)
        weights = self.plan.phases[phases]  # (J, K)
        return np.einsum("jkc,jk->jc", samples, weights)

    def process_block(self, block: np.ndarray) -> np.ndarray:
        """Feed one input block; return every output block now computable."""
        self._append(block)
        channels = self._buffer.shape[1]
        if self.plan.passthrough:
            out = self._buffer.copy()
            self._buffer = np.empty((0, channels))
            self._offset = self._total_in
            self._n_out += out.shape[0]
            return out

        end_index = self._offset + self._buffer.shape[0] - 1
        j_max = self._emittable_count(end_index)
        if j_max <= self._n_out:
            emitted = np.empty((0, channels))
        else:
            j_indices = np.arange(self._n_out, j_max)
            emitted = self._gather(j_indices, tail_pad=False)
            self._n_out = j_max

        # Drop only frames strictly before the next output's first tap.
        _, next_start, _ = self._next_geometry()
        if next_start > self._offset:
            drop = min(next_start - self._offset, self._buffer.shape[0])
            self._buffer = self._buffer[drop:]
            self._offset += drop
        return emitted

    def _emittable_count(self, end_index: int) -> int:
        """One-past count of output frames whose last tap is available."""
        up, down = self.plan.up, self.plan.down
        bound = end_index - HALF_TAPS
        if bound < 0:
            return self._n_out
        # Largest j with floor(j*D/L) <= bound: j*D < (bound+1)*L.
        j_limit = ((bound + 1) * up - 1) // down
        return max(self._n_out, int(j_limit) + 1)

    def flush(self) -> np.ndarray:
        """Emit remaining outputs with tail zero padding, trimmed to length."""
        expected = self.plan.expected_frames(self._total_in)
        channels = max(self._buffer.shape[1], 1)
        if self.plan.passthrough:
            out = self._buffer.copy()
            self._buffer = np.empty((0, channels))
            self._offset = self._total_in
            self._n_out += out.shape[0]
            return out[:expected]

        j_indices = np.arange(self._n_out, expected)
        if j_indices.size:
            emitted = self._gather(j_indices, tail_pad=True)
            self._n_out = expected
        else:
            emitted = np.empty((0, channels))
        self._buffer = np.empty((0, channels))
        self._offset = self._total_in
        return emitted[:expected]
