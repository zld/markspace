"""Container sniffer and per-file probe entry point.

One bad file (unknown signature, corrupt header, element past end of file,
...) produces a structured error record; it never aborts the rest of the
batch.  Files are opened in binary mode, read in bounded chunks and their
bytes are only interpreted as container structure, never executed.
"""

from __future__ import annotations

import hashlib
import os
from dataclasses import dataclass, field

from .formats import (
    ContainerError,
    MAX_FILE_SIZE,
    parse_matroska,
    parse_mp4,
)
from .tracks import Track, build_tracks

_MP4_FTYP_OFFSET = 4
_EBML_SIGNATURE = b"\x1a\x45\xdf\xa3"
_HASH_CHUNK = 1024 * 1024  # 1 MiB streaming reads keep memory bounded


@dataclass(slots=True)
class ProbeResult:
    path: str
    ok: bool
    container: str | None = None
    error: str | None = None
    duration_ms: int | None = None
    sha256: str | None = None
    file_size: int = 0
    tracks: list[Track] = field(default_factory=list)

    def to_dict(self) -> dict:
        return {
            "path": self.path,
            "ok": self.ok,
            "container": self.container,
            "error": self.error,
            "duration_ms": self.duration_ms,
            "sha256": self.sha256,
            "file_size": self.file_size,
            "tracks": [t.to_dict() for t in self.tracks],
        }


def sniff_container(f, file_size: int) -> str:
    """Identify the container from leading signature bytes."""
    if file_size < 12:
        raise ContainerError("file too small for any supported container")
    f.seek(0)
    head = f.read(12)
    if head[_MP4_FTYP_OFFSET:_MP4_FTYP_OFFSET + 4] == b"ftyp":
        return "mp4"
    if head[:4] == _EBML_SIGNATURE:
        return "matroska"
    raise ContainerError("unknown container signature")


def hash_file(path: str) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as f:
        while chunk := f.read(_HASH_CHUNK):
            digest.update(chunk)
    return digest.hexdigest()


def probe_file(path: str) -> ProbeResult:
    """Probe one file, converting every failure into an error record."""
    result = ProbeResult(path=path, ok=False)
    try:
        result.file_size = os.path.getsize(path)
        if result.file_size > MAX_FILE_SIZE:
            result.error = "file exceeds 2 GiB processing limit"
            return result
        with open(path, "rb") as f:
            container = sniff_container(f, result.file_size)
            result.container = container
            if container == "mp4":
                raw = parse_mp4(f, result.file_size)
            else:
                raw = parse_matroska(f, result.file_size)
        result.duration_ms = raw.get("duration_ms")
        result.tracks = build_tracks(raw, path)
        result.sha256 = hash_file(path)
        result.ok = True
    except ContainerError as exc:
        result.ok = False
        result.error = f"container error: {exc}"
        result.tracks = []
    except OSError as exc:
        result.ok = False
        result.error = f"io error: {exc}"
        result.tracks = []
    return result


def probe_many(paths: list[str]) -> list[ProbeResult]:
    """Probe a batch; failures stay isolated to their own records."""
    return [probe_file(path) for path in paths]
