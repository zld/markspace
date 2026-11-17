"""Chunked PCM WAV reading and writing built on the standard ``wave`` module.

Only uncompressed PCM is supported (sample widths 1/2/3/4 bytes, mono or
stereo).  PCM is never loaded whole-file by the higher layers: readers expose
fixed-size sample blocks (2 s by default) and random access to a sample range.
"""

from __future__ import annotations

import wave
from dataclasses import dataclass
from pathlib import Path

import numpy as np

SUPPORTED_WIDTHS = (1, 2, 3, 4)
SUPPORTED_CHANNELS = (1, 2)


class WavFormatError(Exception):
    """Raised when a WAV is compressed, truncated, or otherwise unsupported."""

    def __init__(self, path: str | Path, reason: str):
        self.path = str(path)
        self.reason = reason
        super().__init__(f"{self.path}: {reason}")


@dataclass(frozen=True)
class WavInfo:
    """Header information of a PCM WAV file."""

    path: str
    sample_rate: int
    channels: int
    sample_width: int
    n_frames: int
    duration_seconds: float


def _decode_raw(raw: bytes, sample_width: int, channels: int) -> np.ndarray:
    """Decode little-endian PCM bytes to mono/stereo float32 in [-1.0, 1.0].

    Returns a one-dimensional mono array or a ``(n, channels)`` stereo array.
    """

    if sample_width == 1:
        # 8-bit PCM is unsigned.
        data = np.frombuffer(raw, dtype=np.uint8).astype(np.float32)
        samples = (data - 128.0) / 128.0
    elif sample_width == 2:
        data = np.frombuffer(raw, dtype="<i2").astype(np.float32)
        samples = data / 32768.0
    elif sample_width == 3:
        raw_arr = np.frombuffer(raw, dtype=np.uint8).reshape(-1, 3)
        as_i32 = (
            raw_arr[:, 0].astype(np.int32)
            | (raw_arr[:, 1].astype(np.int32) << 8)
            | (raw_arr[:, 2].astype(np.int32) << 16)
        )
        # Sign-extend 24-bit values.
        as_i32 = np.where(as_i32 & 0x800000, as_i32 - 0x1000000, as_i32)
        samples = as_i32.astype(np.float32) / 8388608.0
    else:  # sample_width == 4
        data = np.frombuffer(raw, dtype="<i4").astype(np.float32)
        samples = data / 2147483648.0

    if channels == 2:
        return samples.reshape(-1, 2)
    return samples


def _encode_samples(samples: np.ndarray, sample_width: int) -> bytes:
    """Encode mono float samples in [-1.0, 1.0] to little-endian PCM bytes."""

    clipped = np.clip(samples, -1.0, 1.0)
    if sample_width == 1:
        quantized = np.round(clipped * 128.0 + 128.0).astype(np.uint8)
        return quantized.tobytes()
    if sample_width == 2:
        quantized = np.round(clipped * 32767.0).astype("<i2")
        return quantized.tobytes()
    if sample_width == 3:
        quantized = np.round(clipped * 8388607.0).astype("<i4")
        raw = np.empty((quantized.size, 3), dtype=np.uint8)
        view = quantized.view(np.uint32)
        raw[:, 0] = view & 0xFF
        raw[:, 1] = (view >> 8) & 0xFF
        raw[:, 2] = (view >> 16) & 0xFF
        return raw.tobytes()
    quantized = np.round(clipped * 2147483647.0).astype("<i4")
    return quantized.tobytes()


class WavReader:
    """Read a PCM WAV in sample blocks without loading the whole file."""

    def __init__(self, path: str | Path):
        self.path = str(path)
        try:
            self._wf = wave.open(self.path, "rb")
        except (wave.Error, EOFError, FileNotFoundError) as exc:
            raise WavFormatError(self.path, f"cannot open WAV: {exc}") from exc

        self.channels = self._wf.getnchannels()
        self.sample_width = self._wf.getsampwidth()
        self.sample_rate = self._wf.getframerate()
        self.n_frames = self._wf.getnframes()
        comptype = self._wf.getcomptype()

        if comptype != "NONE":
            self.close()
            raise WavFormatError(
                self.path, f"compressed WAV rejected (comptype={comptype!r})"
            )
        if self.sample_width not in SUPPORTED_WIDTHS:
            self.close()
            raise WavFormatError(
                self.path, f"unsupported sample width {self.sample_width} bytes"
            )
        if self.channels not in SUPPORTED_CHANNELS:
            self.close()
            raise WavFormatError(
                self.path, f"unsupported channel count {self.channels}"
            )
        if self.sample_rate <= 0:
            self.close()
            raise WavFormatError(self.path, "non-positive sample rate")

    @property
    def info(self) -> WavInfo:
        return WavInfo(
            path=self.path,
            sample_rate=self.sample_rate,
            channels=self.channels,
            sample_width=self.sample_width,
            n_frames=self.n_frames,
            duration_seconds=self.n_frames / self.sample_rate,
        )

    def close(self) -> None:
        wf = getattr(self, "_wf", None)
        if wf is not None:
            wf.close()
            self._wf = None

    def __enter__(self) -> "WavReader":
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        self.close()

    def _read_checked(self, n_frames: int) -> bytes:
        raw = self._wf.readframes(n_frames)
        frame_size = self.sample_width * self.channels
        got = len(raw) // frame_size
        if got != n_frames:
            raise WavFormatError(
                self.path,
                f"truncated frame data: expected {n_frames} frames, got {got}",
            )
        return raw

    def iter_blocks(self, block_seconds: float = 2.0):
        """Yield ``(start_sample, mono_float_array)`` blocks of ~block_seconds."""

        block_size = int(round(block_seconds * self.sample_rate))
        if block_size <= 0:
            raise WavFormatError(self.path, "non-positive block size")
        position = self._wf.tell()
        if position != 0:
            self._wf.rewind()
        start = 0
        while start < self.n_frames:
            count = min(block_size, self.n_frames - start)
            raw = self._read_checked(count)
            samples = _decode_raw(raw, self.sample_width, self.channels)
            if self.channels == 2:
                mono = samples.mean(axis=1)
            else:
                mono = samples
            yield start, mono
            start += count

    def read_range_mono(self, start: int, end: int) -> np.ndarray:
        """Random-access read of a mono float sample range [start, end)."""

        if start < 0 or end > self.n_frames or end < start:
            raise WavFormatError(
                self.path, f"sample range [{start}, {end}) out of bounds"
            )
        self._wf.setpos(start)
        raw = self._read_checked(end - start)
        samples = _decode_raw(raw, self.sample_width, self.channels)
        if self.channels == 2:
            return samples.mean(axis=1)
        return samples

    def read_raw_range(self, start: int, end: int) -> bytes:
        """Read raw interleaved PCM bytes of [start, end) for bit-exact copy."""

        if start < 0 or end > self.n_frames or end < start:
            raise WavFormatError(
                self.path, f"sample range [{start}, {end}) out of bounds"
            )
        self._wf.setpos(start)
        return self._read_checked(end - start)


class WavWriter:
    """Write a PCM WAV; the header is finalized on ``close``."""

    def __init__(
        self,
        path: str | Path,
        sample_rate: int,
        channels: int,
        sample_width: int,
    ):
        self.path = str(path)
        if channels not in SUPPORTED_CHANNELS:
            raise WavFormatError(self.path, f"unsupported channels {channels}")
        if sample_width not in SUPPORTED_WIDTHS:
            raise WavFormatError(
                self.path, f"unsupported sample width {sample_width}"
            )
        Path(self.path).parent.mkdir(parents=True, exist_ok=True)
        self._wf = wave.open(self.path, "wb")
        self._wf.setnchannels(channels)
        self._wf.setsampwidth(sample_width)
        self._wf.setframerate(sample_rate)
        self.sample_rate = sample_rate
        self.channels = channels
        self.sample_width = sample_width

    def write_raw(self, raw: bytes) -> None:
        self._wf.writeframesraw(raw)

    def write_mono(self, mono_samples: np.ndarray) -> None:
        if self.channels == 2:
            interleaved = np.repeat(mono_samples, 2)
        else:
            interleaved = np.asarray(mono_samples, dtype=np.float32)
        self._wf.writeframesraw(
            _encode_samples(interleaved, self.sample_width)
        )

    def close(self) -> None:
        wf = getattr(self, "_wf", None)
        if wf is not None:
            wf.close()
            self._wf = None

    def __enter__(self) -> "WavWriter":
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        self.close()


def copy_segment(
    reader: WavReader,
    writer: WavWriter,
    start: int,
    end: int,
    chunk_seconds: float = 2.0,
) -> None:
    """Copy [start, end) from reader to writer in chunks, raw PCM preserved."""

    if (
        writer.sample_rate != reader.sample_rate
        or writer.channels != reader.channels
        or writer.sample_width != reader.sample_width
    ):
        raise WavFormatError(
            writer.path, "writer format does not match source WAV"
        )
    chunk = max(1, int(round(chunk_seconds * reader.sample_rate)))
    cursor = start
    while cursor < end:
        step = min(chunk, end - cursor)
        writer.write_raw(reader.read_raw_range(cursor, cursor + step))
        cursor += step
