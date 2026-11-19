"""PCM WAV reading and writing through SoundFile.

Only uncompressed integer PCM WAV is accepted.  Floating-point, other
subtypes, more than 8 channels and any compressed subtype are rejected with a
:class:`PipelineError` whose message ends up verbatim in the batch JSON.

Reading is block based: :class:`WavReader` yields ``frames x channels``
float64 blocks from a ``frames``-sized window, so large files are never
materialised as a single array.  ``channels`` is always the second axis
(``always_2d=True``); the interleaved layout of the file is handled by
libsndfile and never confused with the time axis.

Writing targets a temporary ``.part`` file in the destination directory,
which is atomically renamed to the final name only on success.  If
anything fails, the temporary file is removed, so a failed job can never
leave a half-written WAV behind.
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
    "WavWriter",
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


def pcm_grid(subtype: str) -> tuple[int, float]:
    """Return ``(bit depth, scale)`` for a supported output subtype."""
    if subtype == "PCM_16":
        return 16, 32768.0
    if subtype == "PCM_24":
        return 24, 8388608.0
    raise ValueError(f"output subtype must be PCM_16 or PCM_24, got {subtype!r}")


def quantise_pcm(
    block: np.ndarray, subtype: str
) -> tuple[np.ndarray, np.ndarray, int, int]:
    """Quantise float samples onto the integer PCM grid.

    Returns ``(ints, decoded, out_of_range, rail_samples)``:

    * ``ints`` -- integer samples exactly as written;
    * ``decoded == ints / scale`` -- exactly what a reader gets back;
    * ``out_of_range`` -- samples with ``|x| > 1`` *before* quantisation
      (the value that must be reported separately from input clipping);
    * ``rail_samples`` -- decoded samples pinned to the minimum or maximum
      PCM code, i.e. samples sitting on a full-scale rail.

    The mapping is deterministic and independent of libsndfile's own
    float handling.
    """
    bits, scale = pcm_grid(subtype)
    arr = np.asarray(block, dtype=np.float64)
    if not np.all(np.isfinite(arr)):
        raise PipelineError("non_finite_output", "refusing to quantise non-finite samples")
    out_of_range = int(np.count_nonzero(np.abs(arr) > 1.0))
    maximum = (1 << (bits - 1)) - 1
    minimum = -(1 << (bits - 1))
    q = np.clip(np.round(np.clip(arr, -1.0, 1.0) * scale), minimum, maximum)
    rail_samples = int(np.count_nonzero((q <= minimum) | (q >= maximum)))
    dtype = np.int16 if bits == 16 else np.int32
    ints = q.astype(dtype)
    decoded = q / scale
    return ints, decoded, out_of_range, rail_samples


class WavWriter:
    """Atomic block-wise PCM WAV writer.

    ``commit()`` closes the file and renames the temporary ``.part`` file
    to ``path``.  ``abort()`` (also called by ``__exit__`` on error)
    closes and deletes the temporary file.
    """

    def __init__(self, path: str, sample_rate: int, channels: int, subtype: str) -> None:
        if subtype not in ("PCM_16", "PCM_24"):
            raise ValueError(f"output subtype must be PCM_16 or PCM_24, got {subtype!r}")
        self._bits, self._scale = pcm_grid(subtype)
        if not MIN_SAMPLE_RATE <= sample_rate <= MAX_SAMPLE_RATE:
            raise ValueError(f"invalid output sample rate {sample_rate}")
        if not 1 <= channels <= MAX_CHANNELS:
            raise ValueError(f"invalid output channel count {channels}")
        self.path = path
        self.subtype = subtype
        self.frames_written = 0
        # Samples with |x| > 1 before quantisation, and samples actually
        # pinned to a PCM rail.  Both are kept out of the input clipping
        # count so they can be reported separately.
        self.out_of_range_samples = 0
        self.rail_samples = 0
        self._closed = False
        self._committed = False
        directory = os.path.dirname(os.path.abspath(path))
        os.makedirs(directory, exist_ok=True)
        try:
            handle, self._tmp_path = tempfile.mkstemp(
                prefix=".audiopipe-", suffix=".part.wav", dir=directory
            )
            os.close(handle)
            self._file = sf.SoundFile(
                self._tmp_path,
                mode="w",
                samplerate=sample_rate,
                channels=channels,
                subtype=subtype,
                format="WAV",
            )
        except (OSError, sf.LibsndfileError) as exc:
            raise PipelineError("write_failed", f"cannot create output WAV: {exc}") from exc

    def write(self, block: np.ndarray) -> None:
        """Quantise and write one block.

        Quantisation is done here rather than by libsndfile so the
        mapping is fixed by this code and reproducible.  Pre-quantisation
        overshoot and rail-pinned samples are accumulated separately (see
        :func:`quantise_pcm`).
        """
        ints, _decoded, out_of_range, rail_samples = quantise_pcm(block, self.subtype)
        if ints.shape[0] == 0:
            return
        if ints.ndim != 2:
            raise ValueError("expected a frames x channels array")
        self.out_of_range_samples += out_of_range
        self.rail_samples += rail_samples
        if self._bits == 24:
            # 24-bit samples are stored left-justified in 32-bit words.
            ints = (ints.astype(np.int32) << 8).astype(np.int32)
        try:
            self._file.write(ints)
        except (sf.LibsndfileError, OSError, ValueError) as exc:
            raise PipelineError("write_failed", f"error writing WAV: {exc}") from exc
        self.frames_written += ints.shape[0]

    def commit(self) -> None:
        if self._closed:
            raise RuntimeError("writer already closed")
        try:
            self._file.close()
        except (sf.LibsndfileError, OSError) as exc:
            self._closed = True
            self._cleanup()
            raise PipelineError("write_failed", f"error finalising WAV: {exc}") from exc
        self._closed = True
        try:
            os.replace(self._tmp_path, self.path)
        except OSError as exc:
            self._cleanup()
            raise PipelineError(
                "write_failed", f"cannot move output into place: {exc}"
            ) from exc
        self._committed = True

    def abort(self) -> None:
        if self._closed:
            if not self._committed:
                self._cleanup()
            return
        try:
            self._file.close()
        except Exception:  # pragma: no cover - best effort
            pass
        self._closed = True
        self._cleanup()

    def _cleanup(self) -> None:
        try:
            if os.path.exists(self._tmp_path):
                os.unlink(self._tmp_path)
        except OSError:  # pragma: no cover - cleanup must not raise
            pass

    def __enter__(self) -> "WavWriter":
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        if exc_type is None:
            if not self._closed:
                self.abort()
        else:
            self.abort()
