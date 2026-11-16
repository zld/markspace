"""Per-frame hashing and atomic summary export.

Frames are hashed as canonical RGBA bytes (row-major, 4 bytes per
pixel). The summary JSON is written to a temporary sibling file and
atomically renamed into place; on any failure the temp file is removed
so no partial summary is left behind.
"""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path


class ExportError(Exception):
    """Raised when the summary cannot be written safely."""


def frame_sha256(rgba_bytes: bytes) -> str:
    return hashlib.sha256(rgba_bytes).hexdigest()


class SummaryWriter:
    def __init__(self, path):
        self.final = Path(path)
        self.tmp = self.final.with_name(self.final.name + ".tmp")

    def write(self, summary: dict) -> None:
        data = json.dumps(summary, indent=2, sort_keys=True) + "\n"
        try:
            with open(self.tmp, "w", encoding="utf-8") as fh:
                fh.write(data)
                fh.flush()
                os.fsync(fh.fileno())
            os.replace(self.tmp, self.final)
        except OSError as exc:
            self.abort()
            raise ExportError(f"cannot write summary {self.final}: {exc}") from exc

    def abort(self) -> None:
        try:
            self.tmp.unlink(missing_ok=True)
        except OSError:
            pass
