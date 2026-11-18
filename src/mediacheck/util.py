"""Small shared helpers: atomic JSON writes, hashing, path containment."""

from __future__ import annotations

import hashlib
import json
import os
import tempfile
from pathlib import Path

MAX_FILE_SIZE = 2 * 1024 * 1024 * 1024  # 2 GiB
MAX_TRACKS = 64
MAX_BOX_DEPTH = 16


class OutputError(Exception):
    """Raised when a JSON artefact cannot be written."""


def sha256_file(path: Path, chunk_size: int = 1024 * 1024) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        while True:
            chunk = handle.read(chunk_size)
            if not chunk:
                break
            digest.update(chunk)
    return digest.hexdigest()


def atomic_write_json(path: Path, payload: object) -> None:
    """Write UTF-8 JSON and atomically replace the destination.

    The temporary file lives in the destination directory so ``os.replace``
    stays within one filesystem and therefore atomic.  Any filesystem error
    (including permissions and a missing directory) becomes ``OutputError``
    so the CLI can map it to exit code 3.
    """
    directory = path.parent if path.parent != Path("") else Path(".")
    tmp_name: str | None = None
    try:
        directory.mkdir(parents=True, exist_ok=True)
        fd, tmp_name = tempfile.mkstemp(
            prefix=f".{path.name}.", suffix=".tmp", dir=str(directory)
        )
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(payload, handle, ensure_ascii=False, indent=2, sort_keys=False)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(tmp_name, path)
        tmp_name = None
    except (OSError, TypeError, ValueError) as exc:
        raise OutputError(f"cannot write {path}: {exc}") from exc
    finally:
        if tmp_name is not None:
            try:
                os.unlink(tmp_name)
            except OSError:
                pass


def normalise_language(raw: bytes) -> str | None:
    """Decode a ``mdhd`` language field.

    ISO BMFF packs ISO-639-2/T as three 5-bit letters offset from ``0x60``.
    Some muxers write plain ASCII instead; both are accepted.
    """
    if len(raw) != 2:
        return None
    if raw == b"\x00\x00":
        return None
    packed = int.from_bytes(raw, "big")
    letters = [((packed >> shift) & 0x1F) + 0x60 for shift in (10, 5, 0)]
    text = "".join(chr(code) for code in letters)
    if text == "und" or not text.isalpha():
        return None
    return text.lower()


def is_within(root: Path, candidate: Path) -> bool:
    """True when ``candidate`` resolves inside ``root``."""
    try:
        candidate.resolve().relative_to(root.resolve())
    except ValueError:
        return False
    return True
