"""Container identification and dispatch.

``sniff_file`` is the only entry point used by the CLI, ``pack`` and the
tests.  It checks the signature and the 2 GiB cap, then hands the stream to
the WAV or ISO BMFF extractor.  No payload media is ever decoded.
"""

from __future__ import annotations

from pathlib import Path

from .model import SniffResult
from .util import MAX_FILE_SIZE
from .wav import parse_wav_stream

_MP4_FIRST_BOXES = {b"ftyp", b"moov", b"mdat", b"free", b"wide", b"skip", b"pnot"}


def sniff_file(path: str | Path) -> SniffResult:
    path = Path(path)
    try:
        size = path.stat().st_size
    except OSError as exc:
        return SniffResult(container="unknown", size=0, error=f"cannot stat file: {exc}")
    if size > MAX_FILE_SIZE:
        return SniffResult(
            container="unknown",
            size=size,
            error=(
                f"file exceeds {MAX_FILE_SIZE} byte limit "
                f"(size {size}, offset 0)"
            ),
        )
    try:
        with open(path, "rb") as handle:
            head = handle.read(12)
            container = _detect_container(head)
            if container == "wav":
                return parse_wav_stream(handle, file_size=size)
            if container == "mp4":
                return SniffResult(
                    container="mp4",
                    size=size,
                    error="MP4 extraction not available yet",
                )
    except OSError as exc:
        return SniffResult(container="unknown", size=size, error=f"cannot read file: {exc}")
    return SniffResult(
        container="unknown",
        size=size,
        error="unrecognised container signature (offset 0)",
    )


def _detect_container(head: bytes) -> str:
    if len(head) >= 12 and head[:4] == b"RIFF" and head[8:12] == b"WAVE":
        return "wav"
    if len(head) >= 8 and head[4:8] in _MP4_FIRST_BOXES:
        return "mp4"
    return "unknown"
