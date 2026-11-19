"""PCM WAV reading/writing. soundfile is used only for container I/O."""

from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path

import numpy as np
import soundfile as sf

from .config import check_input_limits
from .errors import InputError, ProcessingError

_PCM_SUBTYPES = {"PCM_16", "PCM_24", "PCM_32", "PCM_U8"}


@dataclass(frozen=True)
class Audio:
    """Float64 audio as (frames, channels) plus its sample rate."""

    data: np.ndarray
    rate: int

    @property
    def frames(self) -> int:
        return int(self.data.shape[0])

    @property
    def channels(self) -> int:
        return int(self.data.shape[1])


def read_wav(path: str | os.PathLike[str], *, max_bytes: int) -> Audio:
    """Read a local PCM WAV, enforcing format and size limits."""
    p = Path(path)
    try:
        stat = p.stat()
    except OSError as exc:
        raise InputError(f"cannot stat input: {exc.strerror}", reason="unreadable") from None
    if not p.is_file():
        raise InputError("input is not a regular file", reason="not_a_file")
    try:
        info = sf.info(str(p))
    except Exception as exc:  # soundfile raises RuntimeError/LibsndfileError
        raise InputError(f"unreadable WAV: {exc}", reason="corrupt") from None
    if info.subtype not in _PCM_SUBTYPES:
        raise InputError(
            f"unsupported encoding {info.subtype}; PCM WAV required",
            reason="unsupported_encoding",
        )
    check_input_limits(
        int(info.samplerate),
        int(info.channels),
        int(info.frames),
        int(stat.st_size),
        max_bytes=max_bytes,
    )
    try:
        data, rate = sf.read(str(p), dtype="float64", always_2d=True)
    except Exception as exc:
        raise InputError(f"could not decode WAV: {exc}", reason="corrupt") from None
    if data.ndim != 2 or data.shape[1] != info.channels:
        raise InputError("decoded layout disagrees with header", reason="corrupt")
    return Audio(data=np.ascontiguousarray(data), rate=int(rate))


def clamp_pcm(data: np.ndarray) -> np.ndarray:
    """Clip float samples into the representable [-1, 1] range."""
    return np.clip(data, -1.0, 1.0)


def write_wav_atomic(
    path: str | os.PathLike[str],
    data: np.ndarray,
    rate: int,
    *,
    subtype: str = "PCM_24",
) -> None:
    """Write to a sibling temp file, fsync, then atomically replace the target."""
    target = Path(path)
    target.parent.mkdir(parents=True, exist_ok=True)
    tmp = target.parent / f".{target.name}.tmp-{os.getpid()}"
    try:
        sf.write(str(tmp), clamp_pcm(data), rate, subtype=subtype, format="WAV")
        with open(tmp, "rb") as fh:
            os.fsync(fh.fileno())
        os.replace(tmp, target)
    except OSError as exc:
        _silent_unlink(tmp)
        raise ProcessingError(f"write failed: {exc}", reason="write_error") from None
    except Exception as exc:
        _silent_unlink(tmp)
        raise ProcessingError(f"encoder rejected output: {exc}", reason="write_error") from None


def _silent_unlink(path: Path) -> None:
    try:
        path.unlink(missing_ok=True)
    except OSError:
        pass
