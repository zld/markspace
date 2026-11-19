"""Container sniffing and normalized probe records.

Sniffs WAV and MP4/MOV by file content (not extension), then produces a
JSON-serializable record: container format, container duration and a list
of normalized track dicts. A file that fails to parse yields a record
with errors and no tracks, so one corrupt file never aborts a batch.
"""

from __future__ import annotations

import os

from . import MAX_FILE_BYTES
from .isobmff import parse_mov
from .riff import parse_wav

# Top-level box types that identify an ISO BMFF / QuickTime file when no
# ftyp box leads the file (common in older MOV files).
_MOV_LEADING_BOXES = (b"moov", b"free", b"skip", b"wide", b"mdat", b"pnot")


def sniff(path: str) -> str | None:
    """Return 'wav', 'mov' or None based on the file's leading bytes."""
    try:
        with open(path, "rb") as fh:
            head = fh.read(12)
    except OSError:
        return None
    if len(head) >= 12 and head[:4] in (b"RIFF", b"RF64") and head[8:12] == b"WAVE":
        return "wav"
    if len(head) >= 8 and (head[4:8] == b"ftyp" or head[:4] in _MOV_LEADING_BOXES):
        return "mov"
    return None


def probe_file(path: str) -> dict:
    """Probe one file and return a normalized, JSON-serializable record."""
    record = {
        "path": path,
        "format": None,
        "container_duration_ms": None,
        "tracks": [],
        "errors": [],
    }
    try:
        size = os.path.getsize(path)
    except OSError as exc:
        record["errors"].append(f"cannot stat file: {exc.strerror or exc}")
        return record
    if size > MAX_FILE_BYTES:
        record["errors"].append(f"file exceeds 2 GiB limit ({size} bytes)")
        return record
    kind = sniff(path)
    if kind is None:
        record["errors"].append("unrecognized container (not RIFF/WAVE or ISO BMFF)")
        return record
    record["format"] = kind
    if kind == "wav":
        info = parse_wav(path)
        record["container_duration_ms"] = info.duration_ms
        if info.audio_format is not None:
            record["tracks"].append({
                "kind": "audio",
                "codec": info.codec,
                "duration_ms": info.duration_ms,
                "declared_duration_ms": None,
                "language": None,
                "sample_rate": info.sample_rate,
                "channels": info.channels,
                "bit_depth": info.bit_depth,
                "width": None,
                "height": None,
            })
        record["errors"].extend(info.errors)
    else:
        info = parse_mov(path)
        record["container_duration_ms"] = info.duration_ms
        for t in info.tracks:
            record["tracks"].append({
                "kind": t.kind,
                "codec": t.codec,
                "duration_ms": t.duration_ms,
                "declared_duration_ms": t.declared_duration_ms,
                "language": t.language,
                "sample_rate": t.sample_rate,
                "channels": t.channels,
                "bit_depth": None,
                "width": t.width,
                "height": t.height,
            })
        record["errors"].extend(info.errors)
    return record
