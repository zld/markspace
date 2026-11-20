"""Frame-summary export with bounded parallelism and atomic writes.

The exporter is the only component that touches the output file. It drives
:class:`~timeline_composer.compositor.FrameCompositor` for every frame, hashes
the resulting RGBA bytes, and emits one JSON object per frame. Key properties:

* **Bounded parallelism.** Up to ``workers`` frames are composed at once. Each
  job carries its frame index, so summaries are written strictly in frame
  order regardless of completion order.
* **Fail-fast cleanup.** If a worker raises, the executor is cancelled, the
  temporary summary file is removed, and the error propagates. No partial or
  stale file is left at the destination path.
* **Atomic replace.** Records go to a ``*.tmp`` sibling which is fsynced and
  then ``os.replace``-d over the destination, so a reader never observes a
  half-written summary.
* **No image I/O.** Frames exist only in memory; only the digest survives.
"""

from __future__ import annotations

import hashlib
import json
import os
import tempfile
from concurrent.futures import Future, ThreadPoolExecutor, as_completed
from dataclasses import dataclass
from fractions import Fraction
from pathlib import Path
from typing import Any, Callable, Iterator, Sequence

import numpy as np

from .assets import AssetError
from .audio_sync import AudioSummary, align_events
from .compositor import CompositorError, FrameCompositor
from .schema import Timeline

SUMMARY_VERSION = 1


class ExportError(RuntimeError):
    """Raised when frame summaries cannot be composed or written."""


@dataclass(frozen=True)
class FrameSummary:
    """One line of the summary file: identity plus A/V error for a frame."""

    frame: int
    pts: Fraction
    pixel_sha256: str
    audio_errors_ms: tuple[float, ...]

    def to_dict(self) -> dict[str, Any]:
        return {
            "frame": self.frame,
            "pts": _fraction_text(self.pts),
            "pts_seconds": float(self.pts),
            "pixel_sha256": self.pixel_sha256,
            "audio_errors_ms": list(self.audio_errors_ms),
        }


@dataclass(frozen=True)
class ExportResult:
    """Outcome of a full export run."""

    frame_count: int
    max_abs_error_ms: float
    path: Path
    digest: str


def frame_digest(frame: np.ndarray, width: int, height: int) -> str:
    """SHA-256 over ``width`` x ``height`` RGBA bytes of ``frame``."""
    if frame.shape != (height, width, 4) or frame.dtype != np.uint8:
        raise ExportError(
            f"frame has shape {frame.shape}/{frame.dtype}, expected ({height}, {width}, 4) uint8"
        )
    digest = hashlib.sha256()
    digest.update(np.ascontiguousarray(frame).tobytes())
    return digest.hexdigest()


def build_audio_map(timeline: Timeline) -> tuple[AudioSummary, dict[int, list[float]]]:
    summary = align_events(timeline.clock, timeline.audio_events)
    return summary, summary.errors_by_frame()


def compose_frames(
    composer: FrameCompositor,
    frame_count: int,
    workers: int = 1,
) -> Iterator[FrameSummary]:
    """Yield :class:`FrameSummary` records in frame order.

    Composition errors surface as :class:`ExportError` after any in-flight
    work is cancelled.
    """
    if workers <= 0:
        raise ExportError("workers must be positive")
    if frame_count < 0:
        raise ExportError("frame_count must be non-negative")
    timeline = composer.timeline
    _, audio_map = build_audio_map(timeline)
    canvas = timeline.canvas

    def render(index: int) -> tuple[int, str]:
        pixels = composer.compose(index)
        return index, frame_digest(pixels, canvas.width, canvas.height)

    if workers == 1 or frame_count <= 1:
        for index in range(frame_count):
            _, digest = render(index)
            yield _record(timeline, index, digest, audio_map)
        return

    executor = ThreadPoolExecutor(max_workers=workers, thread_name_prefix="compose")
    pending: dict[Future[tuple[int, str]], int] = {}
    try:
        submitted = 0
        ready: dict[int, str] = {}
        # Keep a bounded window of futures in flight while preserving order.
        max_in_flight = workers * 2
        while submitted < frame_count and len(pending) < max_in_flight:
            pending[executor.submit(render, submitted)] = submitted
            submitted += 1
        next_index = 0
        while pending:
            for future in as_completed(list(pending)):
                pending.pop(future)
                index, digest = future.result()
                ready[index] = digest
                if submitted < frame_count:
                    pending[executor.submit(render, submitted)] = submitted
                    submitted += 1
                while next_index in ready:
                    yield _record(timeline, next_index, ready.pop(next_index), audio_map)
                    next_index += 1
    except BaseException:
        for future in pending:
            future.cancel()
        raise
    finally:
        executor.shutdown(wait=False, cancel_futures=True)


def _record(
    timeline: Timeline,
    index: int,
    digest: str,
    audio_map: dict[int, list[float]],
) -> FrameSummary:
    return FrameSummary(
        frame=index,
        pts=timeline.clock.pts(index),
        pixel_sha256=digest,
        audio_errors_ms=tuple(audio_map.get(index, ())),
    )


def export_summary(
    composer: FrameCompositor,
    destination: str | Path,
    workers: int = 1,
    progress: Callable[[int, int], None] | None = None,
) -> ExportResult:
    """Compose every frame and atomically write the JSONL summary."""
    timeline = composer.timeline
    frame_count = timeline.frame_count
    audio_summary = align_events(timeline.clock, timeline.audio_events)
    target = Path(destination).expanduser()
    try:
        if target.exists() and target.is_dir():
            raise ExportError(f"summary destination is a directory: {target}")
        target.parent.mkdir(parents=True, exist_ok=True)
    except OSError as exc:
        raise ExportError(f"cannot prepare summary destination {target}: {exc}") from exc
    digest_source = hashlib.sha256()
    temp_path: Path | None = None
    try:
        fd, temp_name = tempfile.mkstemp(
            prefix=f".{target.name}.", suffix=".tmp", dir=str(target.parent)
        )
        temp_path = Path(temp_name)
        with os.fdopen(fd, "w", encoding="utf-8", newline="\n") as handle:
            header = _run_header(timeline, frame_count, audio_summary)
            handle.write(json.dumps(header, sort_keys=True, separators=(",", ":")) + "\n")
            digest_source.update(_canonical(header).encode("utf-8"))
            written = 0
            for summary in compose_frames(composer, frame_count, workers):
                payload = summary.to_dict()
                handle.write(json.dumps(payload, sort_keys=True, separators=(",", ":")) + "\n")
                digest_source.update(_canonical(payload).encode("utf-8"))
                written += 1
                if progress is not None and (written % 16 == 0 or written == frame_count):
                    progress(written, frame_count)
            if written != frame_count:
                raise ExportError(f"composed {written} frames, expected {frame_count}")
            handle.flush()
            os.fsync(handle.fileno())
        assert temp_path is not None
        os.replace(temp_path, target)
        temp_path = None
    except (ExportError, AssetError, CompositorError):
        raise
    except BaseException as exc:
        raise ExportError(f"failed to write summary: {exc}") from exc
    finally:
        if temp_path is not None and temp_path.exists():
            try:
                temp_path.unlink()
            except OSError:
                pass
    return ExportResult(
        frame_count=frame_count,
        max_abs_error_ms=audio_summary.max_abs_error_ms,
        path=target,
        digest=digest_source.hexdigest(),
    )


def read_summary(path: str | Path) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    """Parse a summary JSONL file into ``(header, frames)``."""
    source = Path(path)
    try:
        with source.open("r", encoding="utf-8") as handle:
            lines = [line for line in handle.read().splitlines() if line.strip()]
    except OSError as exc:
        raise ExportError(f"cannot read summary {source}: {exc}") from exc
    if not lines:
        raise ExportError("summary file is empty")
    try:
        header = json.loads(lines[0])
        frames = [json.loads(line) for line in lines[1:]]
    except json.JSONDecodeError as exc:
        raise ExportError(f"summary file is not valid JSONL: {exc}") from exc
    if not isinstance(header, dict) or not header.get("kind") == "run":
        raise ExportError("summary header is missing or malformed")
    if not all(isinstance(frame, dict) for frame in frames):
        raise ExportError("summary contains a malformed frame record")
    return header, frames


def _run_header(timeline: Timeline, frame_count: int, audio: AudioSummary) -> dict[str, Any]:
    return {
        "kind": "run",
        "version": SUMMARY_VERSION,
        "canvas": {"width": timeline.canvas.width, "height": timeline.canvas.height},
        "fps": _fraction_text(timeline.fps),
        "frame_count": frame_count,
        "audio_events": audio.event_count,
        "av_max_abs_error_ms": audio.max_abs_error_ms,
    }


def _canonical(payload: dict[str, Any]) -> str:
    return json.dumps(payload, sort_keys=True, separators=(",", ":"))


def _fraction_text(value: Fraction) -> str:
    return f"{value.numerator}/{value.denominator}"


__all__ = [
    "ExportError",
    "ExportResult",
    "FrameSummary",
    "SUMMARY_VERSION",
    "build_audio_map",
    "compose_frames",
    "export_summary",
    "frame_digest",
    "read_summary",
]
