"""PCM WAV reading/writing. soundfile is used only for container I/O."""

from __future__ import annotations

import os
import struct
from dataclasses import dataclass
from pathlib import Path

import numpy as np
import soundfile as sf

from .config import check_input_limits
from .errors import InputError, ProcessingError

_PCM_SUBTYPES = {"PCM_16", "PCM_24", "PCM_32", "PCM_U8"}
_BYTES_PER_SAMPLE = {"PCM_U8": 1, "PCM_16": 2, "PCM_24": 3, "PCM_32": 4}


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


def inspect_riff(handle, file_size: int) -> dict[str, int]:
    """Validate the RIFF chunk layout and return the data chunk bounds.

    libsndfile silently returns fewer frames for a truncated payload, which
    would make a damaged asset look like a valid short one. The chunk list is
    walked with seeks so arbitrarily large metadata chunks are handled without
    trusting the decoder.
    """
    head = handle.read(12)
    if len(head) < 12 or head[8:12] != b"WAVE" or head[0:4] not in (b"RIFF", b"RIFX"):
        raise InputError("not a RIFF/WAVE file", reason="corrupt")
    if head[0:4] == b"RIFX":
        raise InputError("big-endian RIFX is not supported", reason="unsupported_encoding")
    pos = 12
    fmt_seen = False
    while pos + 8 <= file_size:
        handle.seek(pos)
        header = handle.read(8)
        if len(header) < 8:
            break
        chunk_id = header[0:4]
        (chunk_size,) = struct.unpack("<I", header[4:8])
        body = pos + 8
        if chunk_id == b"fmt ":
            fmt_seen = True
        elif chunk_id == b"data":
            if not fmt_seen:
                raise InputError("data chunk before fmt chunk", reason="corrupt")
            available = max(file_size - body, 0)
            if chunk_size > available:
                raise InputError(
                    f"data chunk truncated: declares {chunk_size} bytes, "
                    f"file has {available}",
                    reason="corrupt",
                )
            return {"data_offset": body, "data_bytes": chunk_size, "file_size": file_size}
        if chunk_size > file_size:
            break
        pos = body + chunk_size + (chunk_size & 1)
    raise InputError("no data chunk found", reason="corrupt")


def read_wav(path: str | os.PathLike[str], *, max_bytes: int) -> Audio:
    """Read a local PCM WAV, enforcing format, integrity and size limits."""
    p = Path(path)
    try:
        stat = p.stat()
    except OSError:
        raise InputError("cannot stat input", reason="unreadable") from None
    if not p.is_file():
        raise InputError("input is not a regular file", reason="not_a_file")
    try:
        with open(p, "rb") as handle:
            bounds = inspect_riff(handle, stat.st_size)
    except OSError:
        raise InputError("cannot read input", reason="unreadable") from None
    try:
        info = sf.info(str(p))
    except Exception:
        raise InputError("unreadable WAV header", reason="corrupt") from None
    if info.subtype not in _PCM_SUBTYPES:
        raise InputError(
            f"unsupported encoding {info.subtype}; PCM WAV required",
            reason="unsupported_encoding",
        )
    expected_bytes = int(info.frames) * int(info.channels) * _BYTES_PER_SAMPLE[info.subtype]
    if expected_bytes > bounds["data_bytes"]:
        raise InputError(
            "header/data mismatch: sample data shorter than header claims",
            reason="corrupt",
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
    except Exception:
        raise InputError("could not decode WAV payload", reason="corrupt") from None
    if data.ndim != 2 or data.shape[1] != info.channels or data.shape[0] != info.frames:
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
