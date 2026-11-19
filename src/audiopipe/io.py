"""PCM WAV reading and writing through SoundFile.

Only uncompressed integer PCM WAV is accepted.  Floating-point, other
subtypes, more than 8 channels and any compressed subtype are rejected with a
:class:`PipelineError` whose message ends up verbatim in the batch JSON.

Reading is block based: :class:`WavReader` yields ``frames x channels``
float64 blocks from a ``frames``-sized window, so large files are never
materialised as a single array.  ``channels`` is always the second axis
(``always_2d=True``); the interleaved layout of the file is handled by
libsndfile and never confused with the time axis.

Reading never materialises a whole file and never confuses the channel
axis with time.
"""

from __future__ import annotations

import os
import tempfile
from dataclasses import dataclass
from typing import Iterator

import numpy as np
import soundfile as sf

__all__ = [
    "SUPPORTED_SUBTYPES",
    "WavInfo",
    "WavReader",
    "probe_wav",
    "PipelineError",
]

SUPPORTED_SUBTYPES = ("PCM_16", "PCM_24", "PCM_32")
MIN_SAMPLE_RATE = 8000
MAX_SAMPLE_RATE = 384000
MAX_CHANNELS = 8

# Rail values of each supported encoding once decoded to float64.
# A source sample sitting on one of these rails may already be clipped
# (run-length confirms it); the two rail values are asymmetric because
# two's-complement / unsigned PCM grids are asymmetric.
_RAILS: dict[str, tuple[float, float]] = {
    "PCM_16": (-1.0, 32767 / 32768.0),
    "PCM_24": (-1.0, 8388607 / 8388608.0),
    "PCM_32": (-1.0, 2147483647 / 2147483648.0),
}


def rail_values(subtype: str) -> tuple[float, float]:
    """Minimum and maximum representable value of ``subtype`` in float64."""
    try:
        return _RAILS[subtype]
    except KeyError as exc:
        raise ValueError(f"unsupported subtype {subtype!r}") from exc


def count_rail_samples(block: np.ndarray, subtype: str) -> int:
    """Count samples pinned to a full-scale rail.

    For integer PCM the decoded float grid is asymmetric, so
    ``-1.0`` and ``(2**n-1)/2**n`` are the only values a clipped source
    can hold.  Comparing against those exact rails (rather than
    ``|x| > 1``, which integer PCM can never satisfy) is what makes the
    input clipping count meaningful.
    """
    low, high = rail_values(subtype)
    arr = np.asarray(block)
    # Both rails are exact binary fractions, so equality is safe.  Allow a
    # single-ulp margin for libsndfile's float conversion.
    eps = 1.0 / (1 << 30)
    return int(np.count_nonzero((arr <= low + eps) | (arr >= high - eps)))


class PipelineError(Exception):
    """A per-file failure that must be recorded in the report."""

    def __init__(self, code: str, message: str) -> None:
        super().__init__(message)
        self.code = code
        self.message = message

    def to_dict(self) -> dict:
        return {"code": self.code, "message": self.message}


@dataclass(frozen=True)
class WavInfo:
    sample_rate: int
    channels: int
    frames: int
    subtype: str
    format: str
    path: str


def probe_wav(path: str) -> WavInfo:
    """Validate that ``path`` is a supported PCM WAV and return its header."""
    try:
        info = sf.info(path)
    except (sf.LibsndfileError, OSError) as exc:
        raise PipelineError("unreadable", f"cannot open WAV: {exc}") from exc
    except RuntimeError as exc:  # pragma: no cover - defensive
        raise PipelineError("unreadable", f"cannot open WAV: {exc}") from exc

    if info.format != "WAV":
        # WAVEX is a WAV container with an extended fmt chunk; libsndfile
        # reports it separately, but the audio is still PCM WAV.
        detail = (
            "WAVEX (extended WAV) is not accepted"
            if info.format == "WAVEX"
            else f"unsupported container {info.format!r}"
        )
        raise PipelineError("unsupported_format", detail)
    if info.subtype not in SUPPORTED_SUBTYPES:
        raise PipelineError(
            "unsupported_format",
            f"unsupported WAV encoding {info.subtype!r}; expected PCM",
        )
    if not MIN_SAMPLE_RATE <= info.samplerate <= MAX_SAMPLE_RATE:
        raise PipelineError(
            "invalid_sample_rate",
            f"unsupported sample rate {info.samplerate}; expected "
            f"{MIN_SAMPLE_RATE}..{MAX_SAMPLE_RATE} Hz",
        )
    if not 1 <= info.channels <= MAX_CHANNELS:
        raise PipelineError(
            "unsupported_channels",
            f"unsupported channel count {info.channels}; expected 1..{MAX_CHANNELS}",
        )
    if info.frames < 0:
        raise PipelineError("unreadable", f"invalid frame count {info.frames}")
    return WavInfo(
        sample_rate=int(info.samplerate),
        channels=int(info.channels),
        frames=int(info.frames),
        subtype=str(info.subtype),
        format=str(info.format),
        path=path,
    )


class WavReader:
    """Block-wise float64 reader for a validated PCM WAV."""

    def __init__(self, path: str, *, block_frames: int = 65536) -> None:
        if block_frames <= 0:
            raise ValueError("block_frames must be positive")
        self.path = path
        self.info = probe_wav(path)
        self.block_frames = block_frames
        self.rail_samples = 0
        try:
            self._file = sf.SoundFile(path, mode="r")
        except (sf.LibsndfileError, OSError) as exc:
            raise PipelineError("unreadable", f"cannot open WAV: {exc}") from exc
        if self._file.samplerate != self.info.sample_rate or self._file.channels != self.info.channels:
            self._file.close()
            raise PipelineError("unreadable", "WAV header changed between probe and open")

    @property
    def sample_rate(self) -> int:
        return self.info.sample_rate

    @property
    def channels(self) -> int:
        return self.info.channels

    @property
    def frames(self) -> int:
        return self.info.frames

    def blocks(self) -> Iterator[np.ndarray]:
        try:
            while True:
                block = self._file.read(
                    frames=self.block_frames, dtype="float64", always_2d=True
                )
                if block.shape[0] == 0:
                    break
                if block.shape[1] != self.channels:
                    raise PipelineError(
                        "unreadable",
                        f"channel count changed while reading: {block.shape[1]}",
                    )
                if not np.all(np.isfinite(block)):
                    raise PipelineError(
                        "non_finite_input", "input contains NaN or infinite samples"
                    )
                self.rail_samples += count_rail_samples(block, self.info.subtype)
                yield block
        except sf.LibsndfileError as exc:
            raise PipelineError("unreadable", f"error reading WAV: {exc}") from exc

    def close(self) -> None:
        try:
            self._file.close()
        except Exception:  # pragma: no cover - closing must never mask errors
            pass

    def __enter__(self) -> "WavReader":
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        self.close()
