"""Local media access: metadata, decoded reads, and safe output writes."""

from __future__ import annotations

import os
import tempfile
from dataclasses import dataclass
from pathlib import Path

import numpy as np
import soundfile as sf

from .errors import RenderError


@dataclass(frozen=True)
class SourceInfo:
    path: Path
    rate: int
    frames: int
    channels: int


def probe(path: Path, *, track_id: str) -> SourceInfo:
    """Read the header only; report undecodable or empty media."""
    if not path.exists():
        raise RenderError(f"source file not found: {path}", track_id=track_id,
                          field="path")
    if not path.is_file():
        raise RenderError(f"source path is not a regular file: {path}",
                          track_id=track_id, field="path")
    try:
        info = sf.info(str(path))
    except (sf.LibsndfileError, RuntimeError, OSError) as exc:
        raise RenderError(f"cannot read audio header: {exc}", track_id=track_id,
                          field="path") from exc
    if info.frames <= 0:
        raise RenderError("source file contains no frames", track_id=track_id,
                          field="path")
    if info.channels <= 0:
        raise RenderError("source file reports no channels", track_id=track_id,
                          field="path")
    if info.samplerate <= 0:
        raise RenderError("source file reports an invalid sample rate",
                          track_id=track_id, field="path")
    return SourceInfo(path=path, rate=info.samplerate, frames=info.frames,
                      channels=info.channels)


def read_all(info: SourceInfo, *, track_id: str) -> np.ndarray:
    """Decode a whole source as float64 (frames, channels)."""
    try:
        data, _rate = sf.read(str(info.path), dtype="float64", always_2d=True)
    except (sf.LibsndfileError, RuntimeError, OSError, ValueError) as exc:
        raise RenderError(f"cannot decode audio: {exc}", track_id=track_id,
                          field="path") from exc
    if data.shape[0] == 0:
        raise RenderError("decoded audio contains no frames", track_id=track_id,
                          field="path")
    if data.shape[1] != info.channels:
        raise RenderError(
            f"channel count changed between header ({info.channels}) and decode "
            f"({data.shape[1]})",
            track_id=track_id, field="path",
        )
    if not np.all(np.isfinite(data)):
        raise RenderError("source contains non-finite samples", track_id=track_id,
                          field="path")
    return data


def write_float_wav(path: Path, samples: np.ndarray, rate: int) -> None:
    """Write 32-bit float WAV atomically; never report a partial file as success."""
    data = np.asarray(samples, dtype=np.float64)
    if data.ndim != 2 or data.shape[1] not in (1, 2):
        raise RenderError("output must be 1- or 2-channel audio", field="output")
    if not np.all(np.isfinite(data)):
        raise RenderError("render produced non-finite samples", field="output")
    path = Path(path)
    try:
        path.parent.mkdir(parents=True, exist_ok=True)
    except OSError as exc:
        raise RenderError(f"cannot create output directory: {exc}",
                          field="output") from exc
    handle = None
    tmp_name = None
    try:
        fd, tmp_name = tempfile.mkstemp(prefix=".mixforge-", suffix=".wav",
                                        dir=str(path.parent))
        handle = os.fdopen(fd, "wb")
        sf.write(handle, data, rate, format="WAV", subtype="FLOAT")
        handle.flush()
        os.fsync(handle.fileno())
        handle.close()
        handle = None
        os.replace(tmp_name, path)
        tmp_name = None
    except (sf.LibsndfileError, RuntimeError, OSError, ValueError) as exc:
        raise RenderError(f"cannot write output: {exc}", field="output") from exc
    finally:
        if handle is not None:
            try:
                handle.close()
            except OSError:
                pass
        if tmp_name is not None:
            try:
                os.unlink(tmp_name)
            except OSError:
                pass
