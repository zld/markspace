"""Windowed-sinc sample rate conversion with streaming state.

The resampler is a stateful pull/push filter: input blocks are appended
with :meth:`push`, and converted output is returned as soon as the sinc
window of each pending output frame is fully covered by buffered input.
Because output frame ``i`` depends only on the input *stream* (never on
how the input was chopped into blocks), the produced signal and the total
output frame count are independent of the block size — which is what
makes offline and streaming renders bit-identical.

Anti-aliasing: when downsampling, the sinc kernel is stretched by the
rate ratio so its cutoff tracks the lower Nyquist frequency. The tail
policy is fixed: at ``final=True`` the input is zero-padded beyond its
end and exactly ``round(total_input_frames * ratio)`` frames are emitted.
"""

from __future__ import annotations

import numpy as np


def _blackman(x: np.ndarray, half_width: int) -> np.ndarray:
    """Blackman window over x in [-half_width, half_width]."""
    return (
        0.42
        + 0.5 * np.cos(np.pi * x / half_width)
        + 0.08 * np.cos(2.0 * np.pi * x / half_width)
    )


class SincResampler:
    """Stateful windowed-sinc resampler for multi-channel float audio."""

    def __init__(
        self,
        in_rate: int,
        out_rate: int,
        channels: int,
        half_width: int = 16,
    ) -> None:
        if in_rate <= 0 or out_rate <= 0:
            raise ValueError("sample rates must be positive")
        if channels <= 0:
            raise ValueError("channels must be positive")
        self.in_rate = int(in_rate)
        self.out_rate = int(out_rate)
        self.channels = int(channels)
        self.half_width = int(half_width)
        self.ratio = self.out_rate / self.in_rate
        # Kernel cutoff: 1.0 when upsampling, ratio when downsampling.
        self.cutoff = min(1.0, self.ratio)
        self._buf = np.zeros((0, self.channels), dtype=np.float64)
        self._buf_start = 0  # input frame index of _buf[0]
        self._total_in = 0
        self._next_out = 0
        self._finalized = False

    @property
    def total_input_frames(self) -> int:
        return self._total_in

    def push(self, block: np.ndarray, final: bool = False) -> np.ndarray:
        """Append an input block and return newly available output frames."""
        if self._finalized:
            raise RuntimeError("push after final")
        block = np.asarray(block, dtype=np.float64)
        if block.ndim == 1:
            block = block[:, np.newaxis]
        if block.shape[1] != self.channels:
            raise ValueError(
                f"expected {self.channels} channels, got {block.shape[1]}"
            )
        if len(block):
            self._buf = np.concatenate([self._buf, block], axis=0)
            self._total_in += len(block)
        if final:
            self._finalized = True
        out = self._emit(final)
        self._trim()
        return out

    def _emit(self, final: bool) -> np.ndarray:
        i0 = self._next_out
        if final:
            i1 = int(round(self._total_in * self.ratio))
        else:
            avail_last = self._buf_start + len(self._buf) - 1
            if avail_last < 0:
                i1 = i0
            else:
                # Emit i only while floor(i / ratio) + half_width <= avail_last.
                i1 = int((avail_last - self.half_width + 1) * self.ratio)
                i1 = max(i1, i0)
                while i1 > i0 and (
                    int(np.floor((i1 - 1) / self.ratio)) + self.half_width
                    > avail_last
                ):
                    i1 -= 1
        if i1 <= i0:
            return np.zeros((0, self.channels), dtype=np.float64)
        self._next_out = i1
        return self._render(i0, i1)

    def _render(self, i0: int, i1: int) -> np.ndarray:
        """Render output frames [i0, i1) from the buffered input."""
        m = self.half_width
        idx = np.arange(i0, i1, dtype=np.float64)
        t = idx / self.ratio
        j0 = np.floor(t).astype(np.int64)
        offsets = np.arange(-m + 1, m + 1, dtype=np.int64)
        k = j0[:, np.newaxis] + offsets[np.newaxis, :]  # input indices
        local = k - self._buf_start
        valid = (local >= 0) & (local < len(self._buf))
        gathered = self._buf[np.clip(local, 0, max(len(self._buf) - 1, 0))]
        gathered = gathered * valid[:, :, np.newaxis]
        frac = t[:, np.newaxis] - k
        kernel = (
            self.cutoff
            * np.sinc(self.cutoff * frac)
            * _blackman(frac, m)
        )
        out = (gathered * kernel[:, :, np.newaxis]).sum(axis=1)
        return out

    def _trim(self) -> None:
        """Drop input history no output frame can still reference."""
        if not len(self._buf):
            return
        earliest = int(np.floor(self._next_out / self.ratio)) - self.half_width + 1
        drop = min(earliest - self._buf_start, len(self._buf))
        if drop > 0:
            self._buf = self._buf[drop:]
            self._buf_start += drop


def resample(block: np.ndarray, in_rate: int, out_rate: int) -> np.ndarray:
    """One-shot convenience wrapper around :class:`SincResampler`."""
    block = np.asarray(block, dtype=np.float64)
    channels = 1 if block.ndim == 1 else block.shape[1]
    resampler = SincResampler(in_rate, out_rate, channels)
    return resampler.push(block, final=True)
