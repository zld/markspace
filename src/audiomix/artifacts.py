"""Atomic artifact writing shared by the offline and streaming drivers.

A run may produce a WAV and a JSON report. Both are staged as sibling
temporary files and moved into place together only after every stage
succeeds, so a failed run never leaves a truncated WAV or a report that looks
complete. Any staging failure removes the temporary files. Input files are
never opened for writing.
"""

from __future__ import annotations

import os
import tempfile
from pathlib import Path
from typing import Mapping

import numpy as np
import soundfile as sf

from .errors import OutputError

SUBTYPE = "FLOAT"


def _check_not_input(path: Path, protected: list[Path]) -> None:
    resolved = path.resolve()
    for item in protected:
        if resolved == item.resolve():
            raise OutputError(f"refusing to overwrite input file {path}")


def _umask() -> int:
    current = os.umask(0)
    os.umask(current)
    return current


class ArtifactSet:
    """A transaction over one or more output files.

    Usage::

        with ArtifactSet({"wav": wav_path, "report": report_path}, protected) as art:
            write_wav_direct(art.temp("wav"), samples, rate)
            report_path = art.temp("report")
            ...
            art.commit()

    Entering creates a temp file per target; leaving without ``commit``
    discards every temp file, even on exception.
    """

    def __init__(self, targets: Mapping[str, Path], protected: list[Path] | None) -> None:
        self.targets = dict(targets)
        self.protected = protected or []
        self._temps: dict[str, Path] = {}
        self._committed = False

    def __enter__(self) -> "ArtifactSet":
        for key, target in self.targets.items():
            _check_not_input(target, self.protected)
            try:
                target.parent.mkdir(parents=True, exist_ok=True)
                handle, raw = tempfile.mkstemp(
                    dir=str(target.parent), prefix=f".{target.name}.", suffix=".tmp"
                )
                os.close(handle)
            except OSError as exc:
                self._cleanup()
                raise OutputError(f"cannot stage {target}: {exc}") from exc
            self._temps[key] = Path(raw)
        return self

    def temp(self, key: str) -> Path:
        if key not in self._temps:
            raise KeyError(key)
        return self._temps[key]

    def commit(self) -> None:
        for key, target in self.targets.items():
            temp = self._temps[key]
            # mkstemp creates 0600; give committed artifacts normal umask perms.
            try:
                os.chmod(temp, 0o666 & ~_umask())
            except OSError:
                pass
            os.replace(temp, target)
        self._committed = True
        self._temps.clear()

    def _cleanup(self) -> None:
        for temp in self._temps.values():
            try:
                temp.unlink()
            except OSError:
                pass
        self._temps.clear()

    def __exit__(self, exc_type, exc, tb) -> None:
        if not self._committed:
            self._cleanup()


def write_wav_direct(path: Path, samples: np.ndarray, rate: int) -> None:
    """Write a float32 WAV to exactly ``path`` (staging is the caller's job)."""
    try:
        with sf.SoundFile(
            str(path),
            mode="w",
            samplerate=rate,
            channels=int(samples.shape[1]),
            subtype=SUBTYPE,
            format="WAV",
        ) as handle:
            handle.write(samples.astype(np.float32, copy=False))
    except Exception as exc:
        raise OutputError(f"cannot write WAV {path}: {exc}") from exc


def wav_block_sink(path: Path, channels: int, rate: int):
    """Return a writer callable plus its close function for block streaming.

    Batching blocks through a single open SoundFile avoids re-opening the file
    per block and keeps the on-disk sample order identical to offline output.
    """
    try:
        handle = sf.SoundFile(
            str(path),
            mode="w",
            samplerate=rate,
            channels=channels,
            subtype=SUBTYPE,
            format="WAV",
        )
    except Exception as exc:
        raise OutputError(f"cannot open WAV {path}: {exc}") from exc

    def write(block: np.ndarray) -> None:
        if block.shape[1] != channels:
            raise OutputError(f"block has {block.shape[1]} channel(s), expected {channels}")
        if not np.all(np.isfinite(block)):
            raise OutputError("refusing to write non-finite samples")
        try:
            handle.write(block.astype(np.float32, copy=False))
        except Exception as exc:
            raise OutputError(f"cannot write WAV {path}: {exc}") from exc

    def close() -> None:
        try:
            handle.close()
        except Exception as exc:
            raise OutputError(f"cannot finalise WAV {path}: {exc}") from exc

    return write, close
