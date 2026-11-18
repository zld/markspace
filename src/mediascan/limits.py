"""Hard resource limits for untrusted local media."""

from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True)
class Limits:
    """Every parser boundary check draws from this object.

    Sizes are bytes.  Parsers seek over payloads instead of loading them, so
    ``max_payload_read`` is the only amount read into memory for inspection.
    """

    max_file_size: int = 256 * 1024 * 1024
    max_boxes: int = 20_000
    max_depth: int = 16
    max_tracks: int = 64
    max_sample_entries: int = 32
    max_payload_read: int = 1 * 1024 * 1024
    max_chunk: int = 1024 * 1024

    def check_file_size(self, size: int) -> None:
        if size > self.max_file_size:
            raise _too_large(size, self.max_file_size)


def _too_large(size: int, limit: int):
    # Imported lazily to keep this module dependency-free at import time.
    from .errors import ERR_FILE_TOO_LARGE, ScanFailure

    return ScanFailure(
        ERR_FILE_TOO_LARGE,
        f"file size {size} exceeds limit {limit}",
        offset=0,
    )
