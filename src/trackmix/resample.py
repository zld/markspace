"""Stateful windowed-sinc sample-rate conversion.

The resampler is a *pull* device: the caller asks for output frames and
:meth:`SincResampler.required_input_for` reports how many source frames
must have been fed first.  All state (the input history buffer, its
source-frame origin, and the fractional output phase) lives on the
object, so feeding the source in one long offline call or in many short
streaming blocks gives identical output samples.  When the source runs
out the caller feeds explicit zero frames — the fixed tail rule — and
the object can be drained by pulling :meth:`flush_len` extra frames.

The filter is a Hann-windowed sinc evaluated per output at the current
fractional phase.  Coefficients are normalized per output so passband
amplitude is preserved for any rate pair, including upsampling.
"""

from __future__ import annotations

import numpy as np

from .errors import AudioError

TAPS = 32
"""Number of sinc taps (even).  Half extends each side of the centre."""

HALF = TAPS // 2
_PREFILL = HALF - 1


def _hann_window(offset: np.ndarray) -> np.ndarray:
    # offset is in (-1, 1] relative to the window centre.
    return 0.5 * (1.0 + np.cos(np.pi * offset))


class SincResampler:
    """Streaming resampler from ``in_rate`` Hz to ``out_rate`` Hz."""

    def __init__(self, in_rate: int, out_rate: int, channels: int):
        if in_rate <= 0 or out_rate <= 0:
            raise AudioError(
                f"resampler rates must be positive, got {in_rate}->{out_rate}"
            )
        if channels <= 0:
            raise AudioError(f"resampler channel count must be positive: {channels}")
        self.in_rate = int(in_rate)
        self.out_rate = int(out_rate)
        self.channels = int(channels)
        # History buffer, newest sample last, plus the source-frame index
        # of buffer row 0.  Prefilled zeros represent frames -_PREFILL..-1
        # so the first output can reach backwards before frame 0.
        self._buffer = np.zeros((_PREFILL, channels), dtype=np.float64)
        self._origin = -_PREFILL
        self._fed = 0  # total frames ever fed (real or zero-pad)
        self._produced = 0  # total output frames ever produced
        self._ratio = self.in_rate / self.out_rate

    # ------------------------------------------------------------------ #
    # State queries
    # ------------------------------------------------------------------ #
    def required_input_for(self, n_out: int) -> int:
        """Source frames that must be fed before reading ``n_out`` frames."""
        if n_out <= 0:
            return self._fed
        last_index = self._produced + n_out - 1
        centre = (last_index + 0.5) * self._ratio - 0.5
        # Newest tap lands on floor(centre); feeding count N makes
        # source frames 0..N-1 available, so N >= floor(centre) + 1.
        return max(self._fed, int(np.floor(centre)) + 1)

    def flush_len(self, total_source_frames: int) -> int:
        """Output frames still buffered after ``total_source_frames`` input."""
        expected = int(np.ceil(total_source_frames * self.out_rate / self.in_rate))
        return max(0, expected - self._produced)

    @property
    def fed(self) -> int:
        return self._fed

    @property
    def produced(self) -> int:
        return self._produced

    # ------------------------------------------------------------------ #
    # Data flow
    # ------------------------------------------------------------------ #
    def feed(self, frames: np.ndarray) -> None:
        """Append source frames, shaped ``(n, channels)`` (mono is (n,1))."""
        frames = np.asarray(frames, dtype=np.float64)
        if frames.ndim != 2 or frames.shape[1] != self.channels:
            raise AudioError(
                f"resampler feed expects (n, {self.channels}); got {frames.shape}"
            )
        if not np.all(np.isfinite(frames)):
            raise AudioError("resampler input contains NaN or infinite samples")
        if frames.shape[0]:
            self._buffer = np.concatenate((self._buffer, frames), axis=0)
            self._fed += frames.shape[0]

    def read(self, n_out: int) -> np.ndarray:
        """Produce ``n_out`` output frames.

        The caller must have fed at least :meth:`required_input_for`
        frames (zero frames are used past the end of the source).
        """
        if n_out <= 0:
            return np.empty((0, self.channels), dtype=np.float64)
        if self._fed < self.required_input_for(n_out):
            raise AudioError(
                f"resampler underrun: {self._fed} frames fed, "
                f"{self.required_input_for(n_out)} required"
            )

        out_indices = np.arange(
            self._produced, self._produced + n_out, dtype=np.float64
        )
        centres = (out_indices + 0.5) * self._ratio - 0.5
        base = np.floor(centres).astype(np.int64)  # newest tap index
        k = np.arange(TAPS, dtype=np.int64)
        positions = base[:, None] - k[None, :]  # (n_out, TAPS)
        x = positions.astype(np.float64) - centres[:, None]
        coeffs = _hann_window(x / HALF) * np.sinc(x)
        coeffs /= np.sum(coeffs, axis=1, keepdims=True)

        # Buffer row for source frame f is f - origin.
        buf_idx = positions - self._origin
        valid = (buf_idx >= 0) & (buf_idx < self._buffer.shape[0])
        safe_idx = np.where(valid, buf_idx, 0)
        gathered = self._buffer[safe_idx]  # (n_out, TAPS, channels)
        gathered = np.where(valid[:, :, None], gathered, 0.0)
        out = np.einsum("ij,ijk->ik", coeffs, gathered)

        self._produced += n_out
        self._trim(base[-1])
        return out

    # ------------------------------------------------------------------ #
    # Internals
    # ------------------------------------------------------------------ #
    def _trim(self, newest_centre: int) -> None:
        """Drop history that no future output can reach back into.

        The oldest tap of this read is ``newest_centre - TAPS + 1``; all
        later centres can only need newer frames.
        """
        new_origin = newest_centre - TAPS + 1
        drop = new_origin - self._origin
        if drop > 0:
            self._buffer = self._buffer[drop:]
            self._origin = new_origin
