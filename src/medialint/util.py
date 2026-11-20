"""Shared helpers: path confinement, hashing, atomic writes."""
from __future__ import annotations

import hashlib
import os
from pathlib import Path

from .model import ManifestError, MediaError

MAX_FILE_BYTES = 512 * 1024 * 1024


def safe_join(root: Path, rel: str) -> Path:
    """Resolve rel under root; refuse absolute paths and '..' escapes."""
    p = Path(rel)
    if p.is_absolute() or ".." in p.parts:
        raise ManifestError(f"unsafe asset path {rel!r}: must be relative and stay inside the input directory")
    root_resolved = root.resolve()
    resolved = (root_resolved / p).resolve()
    if resolved != root_resolved and root_resolved not in resolved.parents:
        raise ManifestError(f"unsafe asset path {rel!r}: escapes the input directory")
    return resolved


def hash_and_read(path: Path, rel: str, max_bytes: int = MAX_FILE_BYTES) -> tuple[str, int, bytes]:
    """Stream the file once: sha256, size and full contents (bounded)."""
    h = hashlib.sha256()
    chunks: list[bytes] = []
    total = 0
    with open(path, "rb") as f:
        while True:
            buf = f.read(1 << 20)
            if not buf:
                break
            total += len(buf)
            if total > max_bytes:
                raise MediaError("limit-exceeded", f"{rel}: file exceeds {max_bytes} byte limit")
            h.update(buf)
            chunks.append(buf)
    return h.hexdigest(), total, b"".join(chunks)


def atomic_write_text(path: Path, text: str) -> None:
    """Write via a temp file + rename so an interrupted write never looks complete."""
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + ".tmp")
    tmp.write_text(text, encoding="utf-8")
    os.replace(tmp, path)
