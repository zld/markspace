"""Frame rendering orchestration and checksum-only JSONL export.

No PNG, video or audio bytes are ever written: each rendered frame is reduced
to a SHA-256 digest over its contiguous RGBA bytes together with its exact
presentation timestamp.  Rendering runs in a bounded thread pool and output is
assembled in frame-index order so any worker count yields an identical file.
"""

from __future__ import annotations

import hashlib
import json
import os
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from pathlib import Path

from .compositor import CompositorError, render_frame
from .model import Timeline
from .timebase import FrameClock, align_cues, summarise_av

MIN_WORKERS, MAX_WORKERS = 1, 8


class ExportError(RuntimeError):
    """Raised when rendering or writing the frame summary fails."""


@dataclass(frozen=True)
class FrameRecord:
    index: int
    pts_us: int
    pixel_sha256: str

    def to_json(self) -> dict:
        return {
            "type": "frame",
            "index": self.index,
            "pts_us": self.pts_us,
            "pixel_sha256": self.pixel_sha256,
        }


def _hash_frame(canvas) -> str:
    # render_frame guarantees a C-contiguous RGBA uint8 buffer.
    return hashlib.sha256(canvas.tobytes()).hexdigest()


def _render_one(timeline: Timeline, assets: dict, clock: FrameClock,
                index: int) -> FrameRecord:
    try:
        canvas = render_frame(timeline, assets, clock, index)
    except CompositorError:
        raise
    except Exception as e:  # defensive: any numpy/asset failure aborts the export
        raise CompositorError(f"frame {index}: rendering failed: {e}") from e
    if len(canvas.shape) != 3 or canvas.shape[2] != 4 or not canvas.flags["C_CONTIGUOUS"]:
        raise CompositorError(f"frame {index}: renderer produced a non-contiguous RGBA buffer")
    digest = _hash_frame(canvas)
    if len(digest) != 64:
        raise CompositorError(f"frame {index}: digest length {len(digest)} != 64")
    return FrameRecord(index=index, pts_us=clock.pts_us(index), pixel_sha256=digest)


def render_all(timeline: Timeline, assets: dict, clock: FrameClock,
               workers: int) -> list[FrameRecord]:
    """Render every frame in a thread pool; first failure cancels the rest."""
    if not (MIN_WORKERS <= workers <= MAX_WORKERS):
        raise ExportError(f"workers must be in {MIN_WORKERS}..{MAX_WORKERS}, got {workers}")
    records: dict[int, FrameRecord] = {}
    with ThreadPoolExecutor(max_workers=workers) as pool:
        futures = {
            pool.submit(_render_one, timeline, assets, clock, i): i
            for i in range(clock.count)
        }
        try:
            for fut, idx in futures.items():
                records[idx] = fut.result()  # raises the first rendering error
        except BaseException:
            for fut in futures:
                fut.cancel()
            raise
    return [records[i] for i in range(clock.count)]


def build_summary(timeline: Timeline, clock: FrameClock,
                  records: list[FrameRecord]) -> dict:
    alignments = align_cues(timeline, clock)
    av = summarise_av(alignments)
    return {
        "type": "summary",
        "frame_count": len(records),
        "fps": f"{clock.fps.numerator}/{clock.fps.denominator}",
        "cue_count": len(timeline.cues),
        "max_abs_av_error_ms": round(av["max_abs_av_error_ms"], 6),
        "mean_abs_av_error_ms": round(av["mean_abs_av_error_ms"], 6),
    }


def export_frames(timeline: Timeline, assets: dict, clock: FrameClock,
                  output: str | Path, workers: int) -> tuple[list[FrameRecord], dict]:
    """Render all frames and atomically write frame records plus a summary.

    On any failure the temporary output is removed and nothing is left behind.
    """
    output = Path(output)
    output.parent.mkdir(parents=True, exist_ok=True)
    tmp = output.with_name(output.name + ".tmp")
    try:
        records = render_all(timeline, assets, clock, workers)
        summary = build_summary(timeline, clock, records)
        with open(tmp, "w", encoding="utf-8") as fh:
            for rec in records:
                fh.write(json.dumps(rec.to_json(), separators=(",", ":")) + "\n")
            fh.write(json.dumps(summary, separators=(",", ":")) + "\n")
            fh.flush()
            os.fsync(fh.fileno())
        os.replace(tmp, output)
    except BaseException:
        # Cancel work already handled inside the pool context; clean files too.
        if tmp.exists():
            try:
                tmp.unlink()
            except OSError:
                pass
        if output.exists() and output != tmp:
            # Never overwrite a previous good export with a half-finished run.
            pass
        raise
    return records, summary
