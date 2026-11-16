"""Directory scanning and chunked audio I/O.

Only WAV and FLAC containers are accepted, with mono or interleaved stereo
streams. Files are processed in stable relative-path order. The module guards
against:

* symbolic links that resolve outside the input root ("link escape"),
* two inputs mapping to the same output path (``a.wav`` + ``a.flac``),
* unsupported extensions and unsupported channel counts.

All reads/writes are frame-blocked: memory use grows with the block size, not
with clip duration. Outputs are written to a temporary sibling file and
atomically replaced only after a successful write.
"""

from __future__ import annotations

import hashlib
import os
from dataclasses import dataclass
from pathlib import Path

import numpy as np
import soundfile as sf

SUPPORTED_EXTS = (".wav", ".flac")
OUTPUT_EXT = ".wav"
# Explicit engine delivery format, see soundfile available subtypes.
OUTPUT_SUBTYPE = "PCM_24"
HASH_CHUNK = 1 << 20  # 1 MiB hashing buffer


class AudioInputError(ValueError):
    """Raised for rejected input layout, links or unsupported formats."""


@dataclass(frozen=True)
class InputFile:
    """One accepted input file with its relative path preserved."""

    path: Path
    relative: Path  # relative to the scan root, POSIX-style ordering key


def _is_within(root: Path, candidate: Path) -> bool:
    try:
        return os.path.commonpath([str(root.resolve()), str(candidate)]) == str(
            root.resolve()
        )
    except ValueError:  # different drives (defensive; POSIX-only target)
        return False


def scan_input(input_dir: Path) -> list[InputFile]:
    """Recursively collect supported files in sorted relative-path order.

    Raises :class:`AudioInputError` on symlink escape, duplicate output
    targets or unsupported channel layouts. Empty/zero-frame files are kept
    here and reported per-file later by the pipeline.
    """
    root = Path(input_dir)
    if not root.is_dir():
        raise AudioInputError(f"input directory not found: {root}")
    root_real = root.resolve()

    found: list[InputFile] = []
    for dirpath, dirnames, filenames in os.walk(root, followlinks=False):
        here = Path(dirpath)
        # Reject linked directories that walk would otherwise descend into.
        for name in sorted(dirnames):
            linked = here / name
            if linked.is_symlink() and not _is_within(root_real, linked.resolve()):
                raise AudioInputError(f"symlink escapes input root: {linked}")
        for name in sorted(filenames):
            file_path = here / name
            if file_path.is_symlink() and not _is_within(
                root_real, file_path.resolve()
            ):
                raise AudioInputError(f"symlink escapes input root: {file_path}")
            ext = file_path.suffix.lower()
            if ext not in SUPPORTED_EXTS:
                raise AudioInputError(f"unsupported input format: {file_path}")
            info = sf.info(str(file_path))
            if info.channels not in (1, 2):
                raise AudioInputError(
                    f"unsupported channel count {info.channels}: {file_path}"
                )
            found.append(InputFile(file_path, file_path.relative_to(root)))

    found.sort(key=lambda item: item.relative.as_posix())
    seen: set[Path] = set()
    for item in found:
        target = item.relative.with_suffix(OUTPUT_EXT)
        if target in seen:
            raise AudioInputError(f"duplicate output target: {target}")
        seen.add(target)
    return found


def output_path_for(output_dir: Path, item: InputFile) -> Path:
    """Map an input relative path to its WAV output path."""
    return Path(output_dir) / item.relative.with_suffix(OUTPUT_EXT)


def hash_file(path: Path) -> str:
    """Return the SHA-256 hex digest of a file, streamed in 1 MiB chunks."""
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(HASH_CHUNK), b""):
            digest.update(chunk)
    return digest.hexdigest()


def read_blocks(path: Path, block_frames: int):
    """Yield ``(frames, channels)`` float64 blocks of constant channel count.

    Mono files yield shape ``(n, 1)`` so downstream code has one shape.
    """
    with sf.SoundFile(str(path), "r") as handle:
        for block in handle.blocks(blocksize=block_frames, dtype="float64",
                                   always_2d=True, fill_value=0.0):
            yield np.asarray(block, dtype=np.float64)


def probe_file(path: Path) -> sf.SoundFileInfo:
    """Return container metadata without decoding sample data."""
    return sf.info(str(path))


class AtomicWavWriter:
    """Write a WAV to a temp sibling, then atomically replace the target."""

    def __init__(self, target: Path, sample_rate: int, channels: int):
        self.target = Path(target)
        self.sample_rate = int(sample_rate)
        self.channels = int(channels)
        self.tmp_path = self.target.with_name(
            f".{self.target.name}.tmp-{os.getpid()}"
        )
        self._handle: sf.SoundFile | None = None

    def __enter__(self) -> "AtomicWavWriter":
        self.target.parent.mkdir(parents=True, exist_ok=True)
        self._handle = sf.SoundFile(
            str(self.tmp_path),
            mode="w",
            samplerate=self.sample_rate,
            channels=self.channels,
            subtype=OUTPUT_SUBTYPE,
            format="WAV",
        )
        return self

    def write(self, block: np.ndarray) -> None:
        assert self._handle is not None, "writer used outside context"
        self._handle.write(np.asarray(block, dtype=np.float64))

    def __exit__(self, exc_type, exc, tb) -> None:
        assert self._handle is not None
        self._handle.close()
        if exc_type is None:
            os.replace(self.tmp_path, self.target)
        else:
            # Failure path: never leave a partial deliverable behind.
            self.tmp_path.unlink(missing_ok=True)
