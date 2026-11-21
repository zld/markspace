"""Recursive, deterministic input discovery with conflict checks."""

from __future__ import annotations

import hashlib
import os
import stat
from dataclasses import dataclass
from pathlib import Path

import soundfile as sf

from .errors import TaskError
from .resample import channels_label, is_supported_rate

SUPPORTED_EXTENSIONS = {".wav": "WAV", ".flac": "FLAC", ".aiff": "AIFF", ".aif": "AIFF"}
SUPPORTED_CHANNELS = (1, 2, 6)
HASH_CHUNK = 1 << 20


@dataclass(frozen=True)
class InputFile:
    source: Path
    relative_path: str
    ext_format: str
    frames: int
    sample_rate: int
    channels: int
    subtype: str

    @property
    def channel_layout(self) -> str:
        return channels_label(self.channels)

    def as_dict(self) -> dict[str, object]:
        return {
            "relative_path": self.relative_path,
            "format": self.ext_format,
            "subtype": self.subtype,
            "frames": self.frames,
            "sample_rate": self.sample_rate,
            "channels": self.channels,
            "channel_layout": self.channel_layout,
        }


@dataclass
class Discovery:
    files: list[InputFile]
    errors: list[TaskError]

    @property
    def failed_count(self) -> int:
        return len(self.errors)


def _is_symlink(path: Path) -> bool:
    try:
        return stat.S_ISLNK(os.lstat(path).st_mode)
    except OSError:
        return True


def check_output_relative(relative_path: str) -> str:
    """Map a relative source path to its output path, rejecting collisions.

    ``a/b.wav`` and ``a/b.AIFF`` would both land on ``a/b.wav``; the flat
    namespace check makes such clashes explicit instead of silently
    overwriting a product.
    """
    posix = Path(relative_path)
    return posix.with_suffix(".wav").as_posix()


def _probe(path: Path, relative_path: str) -> InputFile:
    try:
        info = sf.info(str(path))
    except Exception as exc:  # soundfile raises LibsndfileError and OSError alike
        raise TaskError(relative_path, "discover", f"unreadable or corrupt audio: {exc}") from exc
    if info.frames <= 0:
        raise TaskError(relative_path, "discover", "file contains no audio frames")
    if not is_supported_rate(info.samplerate):
        raise TaskError(
            relative_path,
            "discover",
            f"unsupported sample rate {info.samplerate} Hz (allowed 8000-192000)",
        )
    if info.channels not in SUPPORTED_CHANNELS:
        raise TaskError(
            relative_path,
            "discover",
            f"unsupported channel layout {info.channels}ch (allowed 1, 2, 6)",
        )
    ext = path.suffix.lower()
    return InputFile(
        source=path,
        relative_path=relative_path,
        ext_format=SUPPORTED_EXTENSIONS[ext],
        frames=int(info.frames),
        sample_rate=int(info.samplerate),
        channels=int(info.channels),
        subtype=str(info.subtype),
    )


def discover(input_dir: Path) -> Discovery:
    """Scan ``input_dir`` recursively, never following symlinks.

    A single bad file is recorded as an error and does not stop the scan.
    """
    files: list[InputFile] = []
    errors: list[TaskError] = []
    by_target: dict[str, list[str]] = {}
    for root, dirnames, filenames in os.walk(input_dir, followlinks=False):
        dirnames[:] = sorted(d for d in dirnames if not _is_symlink(Path(root) / d))
        for name in sorted(filenames):
            path = Path(root) / name
            if path.suffix.lower() not in SUPPORTED_EXTENSIONS:
                continue
            relative = path.relative_to(input_dir).as_posix()
            if _is_symlink(path):
                errors.append(TaskError(relative, "discover", "symbolic links are not accepted"))
                continue
            try:
                item = _probe(path, relative)
            except TaskError as exc:
                errors.append(exc)
                continue
            files.append(item)
            by_target.setdefault(check_output_relative(relative), []).append(relative)

    conflicting: set[str] = set()
    for target, sources in by_target.items():
        if len(sources) > 1:
            conflicting.update(sources)
            for relative in sources:
                others = ", ".join(p for p in sources if p != relative)
                errors.append(
                    TaskError(relative, "discover", f"output name {target} collides with {others}")
                )

    files = [f for f in files if f.relative_path not in conflicting]
    files.sort(key=lambda f: f.relative_path)
    errors.sort(key=lambda e: (e.path, e.stage, e.reason))
    return Discovery(files=files, errors=errors)


def sha256_file(path: Path, chunk: int = HASH_CHUNK) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        while True:
            block = handle.read(chunk)
            if not block:
                break
            digest.update(block)
    return digest.hexdigest()
