"""Windowed-sinc resampling implemented with NumPy.

The SincResampler is stateful: it keeps the filter history (an input
tail buffer) and the fractional read phase across process() calls, so
arbitrary chunk boundaries never change the output and never drop or
duplicate samples.  flush() pads the stream tail with zeros and emits
the remaining frames, giving an exact, deterministic output length.
"""
from __future__ import annotations

import numpy as np

# Positions are evaluated in batches to bound peak memory on long files.
_EMIT_BATCH = 65536


class ResampleError(ValueError):
    """Raised on invalid resampler usage or parameters."""


def output_frames(input_frames, in_rate, out_rate):
    """Exact number of output frames for a finite input stream."""
    return int(round(input_frames * out_rate / in_rate))


def _hann(x):
    """Hann window over x in [-1, 1], zero outside."""
    return np.where(np.abs(x) <= 1.0, 0.5 * (1.0 + np.cos(np.pi * x)), 0.0)


class SincResampler:
    """Stateful windowed-sinc resampler for multichannel float audio.

    Usage: feed input with process(chunk) any number of times, then call
    flush() exactly once.  Feeding the same samples in different chunk
    sizes yields bit-identical output.
    """

    def __init__(self, in_rate, out_rate, channels, half_taps=32):
        if in_rate <= 0 or out_rate <= 0:
            raise ResampleError(f"invalid rates {in_rate}->{out_rate}")
        if channels <= 0:
            raise ResampleError(f"invalid channel count {channels}")
        self.in_rate = int(in_rate)
        self.out_rate = int(out_rate)
        self.channels = int(channels)
        self.half = int(half_taps)
        self.step = self.in_rate / self.out_rate  # input frames per output frame
        # Cutoff below Nyquist of the lower rate: acts as anti-alias
        # low-pass when downsampling, plain interpolation when upsampling.
        self.cutoff = min(1.0, self.out_rate / self.in_rate)
        # buf holds input samples from absolute index `base` onward; it is
        # seeded with `half` zeros so reads near t=0 see left-side history.
        self.base = -self.half
        self.buf = np.zeros((self.half, self.channels))
        self.next_out = 0    # absolute index of the next output frame
        self.in_count = 0    # total input frames received
        self.flushed = False

    def process(self, chunk):
        """Consume one input chunk, return newly available output frames."""
        if self.flushed:
            raise ResampleError("process() called after flush()")
        chunk = np.asarray(chunk, dtype=np.float64)
        if chunk.ndim == 1:
            chunk = chunk[:, None]
        if chunk.ndim != 2 or chunk.shape[1] != self.channels:
            raise ResampleError(
                f"expected (n, {self.channels}) chunk, got shape {chunk.shape}"
            )
        self.buf = np.concatenate([self.buf, chunk])
        self.in_count += chunk.shape[0]
        return self._emit(self.base + self.buf.shape[0])

    def flush(self):
        """Signal end of input; emit the remaining output frames."""
        if self.flushed:
            raise ResampleError("flush() called twice")
        self.flushed = True
        total = output_frames(self.in_count, self.in_rate, self.out_rate)
        # Zero-padding lets the filter window run past the stream end.
        pad = np.zeros((2 * self.half + 2, self.channels))
        self.buf = np.concatenate([self.buf, pad])
        return self._emit(self.base + self.buf.shape[0],
                          max_count=total - self.next_out)

    def _emit(self, limit, max_count=None):
        """Emit outputs whose full filter window lies below absolute `limit`."""
        positions = []
        n = self.next_out
        while max_count is None or len(positions) < max_count:
            p = n * self.step
            if int(np.floor(p)) + self.half > limit - 1:
                break
            positions.append(p)
            n += 1
        if not positions:
            return np.zeros((0, self.channels))
        out = self._render(np.asarray(positions))
        self.next_out += len(positions)
        # Drop history no future window can touch.
        keep_from = int(np.floor(positions[-1])) - self.half + 1 - self.base
        if keep_from > 0:
            self.buf = self.buf[keep_from:]
            self.base += keep_from
        return out

    def _render(self, positions):
        """Evaluate the windowed-sinc sum for an array of read positions."""
        taps = 2 * self.half
        offsets = np.arange(taps)
        chunks = []
        for lo in range(0, len(positions), _EMIT_BATCH):
            p = positions[lo:lo + _EMIT_BATCH]
            k0 = np.floor(p).astype(np.int64) - self.half + 1
            idx = k0[:, None] + offsets[None, :]      # absolute input indices
            d = idx - p[:, None]                       # distance from read phase
            w = self.cutoff * np.sinc(self.cutoff * d) * _hann(d / self.half)
            rel = idx - self.base
            chunks.append((self.buf[rel] * w[:, :, None]).sum(axis=1))
        return np.concatenate(chunks)


def resample(samples, in_rate, out_rate):
    """One-shot helper: resample a complete (n, channels) array."""
    samples = np.asarray(samples, dtype=np.float64)
    if samples.ndim == 1:
        samples = samples[:, None]
    engine = SincResampler(in_rate, out_rate, samples.shape[1])
    return np.concatenate([engine.process(samples), engine.flush()])
