"""Container identification by file header, plus input path safety checks.

Files are accepted only if they are regular files inside the manifest
directory, are not symbolic links and stay below a size cap. The container
type is derived from magic bytes, never from the file extension.
"""
from __future__ import annotations

import os
import stat

EBML_MAGIC = b"\x1a\x45\xdf\xa3"
DEFAULT_MAX_BYTES = 512 * 1024 * 1024
_HEAD_BYTES = 16

CONTAINER_MP4 = "mp4"
CONTAINER_MATROSKA = "matroska"


class SniffError(Exception):
    """A file was rejected or its container could not be identified."""


def _check_path(path: str, base_dir: str, max_bytes: int) -> int:
    """Validate the path and return the file size."""
    if os.path.islink(path):
        raise SniffError(f"symbolic links are not allowed: {path}")
    real = os.path.realpath(path)
    base = os.path.realpath(base_dir)
    try:
        common = os.path.commonpath([real, base])
    except ValueError:
        common = ""
    if common != base:
        raise SniffError(f"path escapes the manifest directory: {path}")
    try:
        st = os.stat(path, follow_symlinks=False)
    except OSError as exc:
        raise SniffError(f"cannot stat {path}: {exc.strerror or exc}") from exc
    if not stat.S_ISREG(st.st_mode):
        raise SniffError(f"not a regular file: {path}")
    if st.st_size > max_bytes:
        raise SniffError(f"file exceeds size limit ({st.st_size} > {max_bytes} bytes): {path}")
    return st.st_size


def sniff(path: str, base_dir: str, max_bytes: int = DEFAULT_MAX_BYTES) -> str:
    """Return the container type for *path* after safety checks."""
    _check_path(path, base_dir, max_bytes)
    try:
        with open(path, "rb") as fh:
            head = fh.read(_HEAD_BYTES)
    except OSError as exc:
        raise SniffError(f"cannot read {path}: {exc.strerror or exc}") from exc
    if len(head) >= 8 and head[4:8] == b"ftyp":
        return CONTAINER_MP4
    if head[:4] == EBML_MAGIC:
        return CONTAINER_MATROSKA
    raise SniffError(f"unrecognized container signature: {path}")
