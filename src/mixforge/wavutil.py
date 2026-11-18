"""Diagnostics: byte-stable WAV output independent of wall-clock time.

soundfile appends a RIFF ``PEAK`` chunk whose timestamp comes from
``time(NULL)``, so two identical renders produce different file bytes even
though every sample matches. The payload (version, timestamp, per-channel
peak floats) is still well-formed; zeroing it makes deliverables reproducible.
"""

from __future__ import annotations

import struct
from pathlib import Path

_PEAK = b"PEAK"
_TIMESTAMP_OFFSET = 4  # after the 4-byte version field


def normalise_wav_chunks(path: Path) -> bool:
    """Zero the PEAK timestamp in-place. Returns True if a chunk was changed.

    Raises ``OSError`` if the file cannot be read or rewritten; callers on a
    best-effort path should catch it. Samples are never modified.
    """
    data = bytearray(path.read_bytes())
    if len(data) < 12 or data[0:4] != b"RIFF" or data[8:12] != b"WAVE":
        return False
    changed = False
    pos = 12
    while pos + 8 <= len(data):
        chunk_id = bytes(data[pos:pos + 4])
        (size,) = struct.unpack_from("<I", data, pos + 4)
        body = pos + 8
        end = body + size
        if end > len(data):
            break
        if chunk_id == _PEAK and size >= 8:
            stamp = body + _TIMESTAMP_OFFSET
            if any(data[stamp:stamp + 4]):
                data[stamp:stamp + 4] = b"\x00\x00\x00\x00"
                changed = True
        pos = end + (size & 1)  # chunks are word-aligned
    if changed:
        path.write_bytes(bytes(data))
    return changed
