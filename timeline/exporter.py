"""Frame-by-frame exporter.

Frames are composed one at a time and immediately reduced to a SHA-256
digest of their normalized RGBA bytes; no pixel or audio data is cached
or persisted. The summary JSON is written to a temporary file first and
atomically moved into place, so a failed run never clobbers a previous
summary.
"""
from __future__ import annotations

import hashlib
import json
import os
import tempfile
from dataclasses import asdict
from typing import Callable

from .audio_sync import SyncStats
from .compositor import compose_frame
from .model import Timeline


class SummaryValidationError(RuntimeError):
    """Raised when the assembled summary fails internal consistency checks."""


class SummaryWriteError(OSError):
    """Raised when the summary cannot be written or moved into place."""


def export_summary(
    timeline: Timeline,
    stats: SyncStats,
    summary_path: str | None = None,
    on_frame: Callable[[int, str], None] | None = None,
) -> dict:
    """Compose every frame, hash it, and optionally persist the summary."""
    frames: list[dict] = []
    for index in range(timeline.frame_count):
        rgba = compose_frame(timeline, index)
        digest = hashlib.sha256(rgba.tobytes()).hexdigest()
        frames.append({"frame_index": index, "sha256": digest})
        if on_frame is not None:
            on_frame(index, digest)
    if len(frames) != timeline.frame_count:
        raise SummaryValidationError(
            f"hashed {len(frames)} frames, expected {timeline.frame_count}"
        )
    summary = {
        "version": 1,
        "fps": timeline.fps,
        "width": timeline.width,
        "height": timeline.height,
        "frame_count": timeline.frame_count,
        "frame_sha256_count": len(frames),
        "frames": frames,
        "audio": asdict(stats),
    }
    if summary_path is not None:
        atomic_write_json(summary_path, summary)
    return summary


def atomic_write_json(path: str, payload: dict) -> None:
    """Write JSON to a temp file in the target directory, then os.replace."""
    directory = os.path.dirname(os.path.abspath(path))
    tmp_path = None
    try:
        os.makedirs(directory, exist_ok=True)
        fd, tmp_path = tempfile.mkstemp(
            dir=directory, prefix=".summary-", suffix=".tmp"
        )
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(payload, handle, indent=2)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(tmp_path, path)
        tmp_path = None
    except OSError as exc:
        raise SummaryWriteError(f"cannot write summary {path!r}: {exc}") from exc
    finally:
        if tmp_path is not None:
            try:
                os.unlink(tmp_path)
            except OSError:
                pass
