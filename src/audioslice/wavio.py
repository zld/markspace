"""Minimal PCM WAV reader and validator (16-bit mono/stereo only).

Only the subset needed for the soundscape pipeline is supported: a canonical
RIFF/WAVE/fmt /data layout, PCM format tag (1), 16-bit samples, one or two
channels. Everything else raises :class:`WavFormatError` carrying the file
name so the batch layer can report which asset failed.
"""

from __future__ import annotations

import struct
import wave
from dataclasses import dataclass

import numpy as np

PCM_FORMAT_TAG = 1
SUPPORTED_SAMPLE_WIDTH = 2  # 16-bit PCM
SUPPORTED_CHANNELS = (1, 2)


class WavFormatError(ValueError):
    """Raised when a WAV file is truncated, empty, or uses an unsupported format."""

    def __init__(self, path: str, reason: str):
        super().__init__(f"{path}: {reason}")
        self.path = path
        self.reason = reason


@dataclass(frozen=True)
class WavData:
    """Decoded audio container.

    ``samples`` is shape ``(n_samples,)`` for mono or
    ``(n_samples, n_channels)`` for stereo, dtype int16.
    """

    path: str
    samples: np.ndarray
    sample_rate: int
    channels: int

    @property
    def n_samples(self) -> int:
        return int(self.samples.shape[0])

    @property
    def duration_seconds(self) -> float:
        return self.n_samples / float(self.sample_rate)


def _read_wav_file(path: str) -> WavData:
    try:
        wf = wave.open(path, "rb")
    except wave.Error as exc:
        raise WavFormatError(path, f"not a valid WAV container: {exc}") from exc
    except OSError as exc:
        raise WavFormatError(path, f"cannot open file: {exc}") from exc

    with wf:
        channels = wf.getnchannels()
        sample_width = wf.getsampwidth()
        sample_rate = wf.getframerate()
        n_frames = wf.getnframes()
        comp_type = wf.getcomptype()

        if comp_type != "NONE":
            raise WavFormatError(
                path, f"unsupported encoding {comp_type!r}; only uncompressed PCM is supported"
            )
        if sample_width != SUPPORTED_SAMPLE_WIDTH:
            raise WavFormatError(
                path, f"unsupported sample width {sample_width * 8}-bit; only 16-bit PCM is supported"
            )
        if channels not in SUPPORTED_CHANNELS:
            raise WavFormatError(
                path, f"unsupported channel count {channels}; only mono and stereo are supported"
            )
        if sample_rate <= 0:
            raise WavFormatError(path, f"invalid sample rate: {sample_rate}")
        if n_frames == 0:
            raise WavFormatError(path, "empty audio: no frames")

        try:
            raw = wf.readframes(n_frames)
        except (wave.Error, OSError) as exc:
            raise WavFormatError(path, f"truncated sample data: {exc}") from exc

    expected_bytes = n_frames * channels * SUPPORTED_SAMPLE_WIDTH
    if len(raw) < expected_bytes:
        raise WavFormatError(
            path,
            f"truncated sample data: expected {expected_bytes} bytes, got {len(raw)}",
        )

    samples = np.frombuffer(raw, dtype="<i2")
    if channels == 2:
        samples = samples.reshape(n_frames, 2)
    return WavData(path=path, samples=samples, sample_rate=sample_rate, channels=channels)


def read_wav(path: str) -> WavData:
    """Read and validate a 16-bit PCM mono/stereo WAV file.

    Raises :class:`WavFormatError` with the offending file name on any
    structural or format problem.
    """
    try:
        return _read_wav_file(path)
    except struct.error as exc:  # pragma: no cover - defensive against malformed headers
        raise WavFormatError(path, f"malformed header: {exc}") from exc
