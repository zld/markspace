"""Atomic artifact writing shared by the offline and streaming drivers.

Artifacts are written to a sibling temporary file and moved into place only
after the whole render succeeds. On any failure the temporary file is removed,
so a failed run never leaves a truncated WAV or a report that looks complete.
Input files are never opened for writing.
"""

from __future__ import annotations

import os
import tempfile
from contextlib import contextmanager
from pathlib import Path
from typing import Iterator

import numpy as np
import soundfile as sf

from .errors import OutputError

SUBTYPE = "FLOAT"


def _check_not_input(path: Path, protected: list[Path]) -> None:
    resolved = path.resolve()
    for item in protected:
        if resolved == item.resolve():
            raise OutputError(f"refusing to overwrite input file {path}")


@contextmanager
def atomic_path(target: Path, protected: list[Path] | None = None) -> Iterator[Path]:
    """Yield a temp path next to ``target``; move it into place on success."""
    if protected:
        _check_not_input(target, protected)
    target.parent.mkdir(parents=True, exist_ok=True)
    handle, raw = tempfile.mkstemp(
        dir=str(target.parent), prefix=f".{target.name}.", suffix=".tmp"
    )
    os.close(handle)
    temp = Path(raw)
    try:
        yield temp
        os.replace(temp, target)
    except BaseException:
        try:
            temp.unlink()
        except OSError:
            pass
        raise


def write_wav_blocked(
    path: Path, blocks: Iterator[np.ndarray], channels: int, rate: int
) -> int:
    """Write float32 WAV from an iterator of blocks; return frames written."""
    frames = 0
    try:
        with sf.SoundFile(
            str(path),
            mode="w",
            samplerate=rate,
            channels=channels,
            subtype=SUBTYPE,
            format="WAV",
        ) as handle:
            for block in blocks:
                if block.shape[1] != channels:
                    raise OutputError(
                        f"block has {block.shape[1]} channel(s), expected {channels}"
                    )
                if not np.all(np.isfinite(block)):
                    raise OutputError("refusing to write non-finite samples")
                handle.write(block.astype(np.float32, copy=False))
                frames += int(block.shape[0])
    except OutputError:
        raise
    except Exception as exc:
        raise OutputError(f"cannot write WAV {path}: {exc}") from exc
    return frames


def write_wav(path: Path, samples: np.ndarray, rate: int, protected: list[Path]) -> None:
    """Atomically write a complete float32 WAV."""
    with atomic_path(path, protected) as temp:
        write_wav_blocked(temp, iter([samples]), int(samples.shape[1]), rate)
