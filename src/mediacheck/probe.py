"""Container sniffing, digesting and dispatch.

The sniffer reads magic bytes, never the filename.  Extensionless and
deliberately mislabelled files therefore probe the same way.
"""

from __future__ import annotations

import hashlib
import os
from pathlib import Path

from .binary import BinaryError
from .matroska import probe_matroska
from .model import Issue, MediaFile, SEVERITY_FATAL, SEVERITY_INFO
from .mp4 import probe_mp4

MAX_FILE_BYTES = 256 * 1024 * 1024
SNIFF_BYTES = 16
CHUNK = 1024 * 1024

MATROSKA_MAGIC = b"\x1a\x45\xdf\xa3"
# Boxes that may legitimately lead a well-formed ISO BMFF file.
LEADING_BOXES = {b"ftyp", b"styp", b"moov", b"mdat", b"free", b"skip",
                 b"wide", b"pnot", b"uuid"}

CONTAINER_BY_EXT = {
    ".mp4": "mp4", ".m4v": "mp4", ".m4a": "mp4", ".mov": "mp4",
    ".mkv": "matroska", ".webm": "matroska", ".mka": "matroska",
}


class ProbeError(Exception):
    """Raised when a file cannot even be sniffed (missing, empty, too big)."""

    def __init__(self, reason: str, offset: int | None = None):
        self.reason = reason
        self.offset = offset
        super().__init__(reason)


def sniff(data: bytes) -> str | None:
    """Return ``"matroska"``, ``"mp4"`` or ``None`` from leading bytes only.

    MP4 detection requires a plausible first box: a known leading fourcc with
    a size the file can contain, or an ``ftyp`` box.  Arbitrary text therefore
    does not pass as a container just because its first 8 bytes parse as a
    length and a fourcc.
    """
    if len(data) >= 4 and data[:4] == MATROSKA_MAGIC:
        return "matroska"
    if len(data) < 8:
        return None
    size32 = int.from_bytes(data[0:4], "big")
    box_type = data[4:8]
    if box_type not in LEADING_BOXES:
        return None
    if size32 == 1:
        if len(data) < 16:
            return None
        size = int.from_bytes(data[8:16], "big")
    elif size32 == 0:
        return None
    else:
        size = size32
    if size < 8:
        return None
    return "mp4"


def _read_all(path: Path, total_size: int) -> bytes:
    with path.open("rb") as handle:
        data = handle.read(MAX_FILE_BYTES + 1)
    if len(data) > MAX_FILE_BYTES:
        raise ProbeError(
            f"file is larger than the {MAX_FILE_BYTES} byte probe limit")
    if len(data) != total_size:
        raise ProbeError("file changed size while being read")
    return data


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        while True:
            chunk = handle.read(CHUNK)
            if not chunk:
                break
            digest.update(chunk)
    return digest.hexdigest()


def probe_path(path: str | os.PathLike[str]) -> MediaFile:
    """Probe one local file and return the unified model.

    Fatal conditions (missing file, empty input, unknown container) are
    reported on the model rather than raised so the CLI can serialise them.
    """
    file_path = Path(path)
    media = MediaFile(path=str(file_path), size=0, sha256=None)
    try:
        stat = file_path.stat()
    except FileNotFoundError:
        media.add_issue(Issue("io.not_found", f"file not found: {file_path}",
                              None, SEVERITY_FATAL))
        return media
    except OSError as exc:
        media.add_issue(Issue("io.unreadable",
                              f"cannot stat {file_path}: {exc.strerror}",
                              None, SEVERITY_FATAL))
        return media
    if not file_path.is_file():
        media.add_issue(Issue("io.not_a_file", f"{file_path} is not a file",
                              None, SEVERITY_FATAL))
        return media
    media.size = stat.st_size
    if stat.st_size == 0:
        media.add_issue(Issue("io.empty_file", "input file is empty", 0,
                              SEVERITY_FATAL))
        return media
    if stat.st_size > MAX_FILE_BYTES:
        media.add_issue(Issue(
            "io.too_large",
            f"file is {stat.st_size} bytes, above the "
            f"{MAX_FILE_BYTES} byte probe limit",
            None, SEVERITY_FATAL))
        return media

    try:
        media.sha256 = sha256_file(file_path)
    except OSError as exc:
        media.add_issue(Issue("io.unreadable",
                              f"cannot read {file_path}: {exc.strerror}",
                              None, SEVERITY_FATAL))
        return media

    try:
        with file_path.open("rb") as handle:
            head = handle.read(SNIFF_BYTES)
    except OSError as exc:
        media.add_issue(Issue("io.unreadable",
                              f"cannot read {file_path}: {exc.strerror}",
                              None, SEVERITY_FATAL))
        return media

    container = sniff(head)
    if container is None:
        media.add_issue(Issue(
            "sniff.unknown_container",
            f"unrecognised container signature {head[:8].hex(' ')}",
            0, SEVERITY_FATAL))
        return media

    try:
        data = _read_all(file_path, stat.st_size)
    except ProbeError as exc:
        media.add_issue(Issue("io.read_failed", exc.reason, exc.offset,
                              SEVERITY_FATAL))
        return media
    except OSError as exc:
        media.add_issue(Issue("io.unreadable",
                              f"cannot read {file_path}: {exc.strerror}",
                              None, SEVERITY_FATAL))
        return media

    media.issues.append(Issue(
        code="sniff.detected",
        message=f"container detected as {container}",
        offset=0,
        severity=SEVERITY_INFO,
    ))
    try:
        if container == "mp4":
            return probe_mp4(data, media)
        return probe_matroska(data, media)
    except BinaryError as exc:  # defensive: probes should catch their own
        media.add_issue(Issue("probe.binary_error", exc.reason, exc.offset,
                              SEVERITY_FATAL))
        return media
