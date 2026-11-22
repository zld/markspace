"""Frame-level rendering manifest built from the resolved canonical model.

The renderer never re-resolves anything: it walks the disjoint
:class:`~timeline_core.model.ResolvedInterval` sequence produced by
:mod:`timeline_core.resolve` and emits one ownership entry per covered
frame. Uncovered frame numbers are simply absent, matching the half-open
coverage model. Each frame also carries the index of the resolved interval
it belongs to so downstream jobs can cross-check render output against the
resolution model.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Iterator

from .model import FrameInterval, ResolutionError, ResolutionResult, ResolvedInterval


@dataclass(frozen=True, slots=True)
class RenderedFrame:
    """Ownership of a single frame, tied back to the resolved interval."""

    frame: int
    clip_id: str
    interval_index: int


@dataclass(frozen=True, slots=True)
class RenderManifest:
    """Every rendered frame plus the interval table it references."""

    fps: int
    frames: tuple[RenderedFrame, ...]
    intervals: tuple[ResolvedInterval, ...]

    @property
    def rendered_frames(self) -> int:
        return len(self.frames)

    def owner_at(self, frame: int) -> str | None:
        """Look up the owning clip for one frame (``None`` if uncovered)."""
        for rendered in self.frames:
            if rendered.frame == frame:
                return rendered.clip_id
            if rendered.frame > frame:
                return None
        return None

    def to_dict(self) -> dict:
        return {
            "fps": self.fps,
            "rendered_frames": self.rendered_frames,
            "intervals": [
                {
                    "start": interval.start,
                    "end": interval.end,
                    "id": interval.clip_id,
                }
                for interval in self.intervals
            ],
            "frames": [
                {
                    "frame": rendered.frame,
                    "id": rendered.clip_id,
                    "interval_index": rendered.interval_index,
                }
                for rendered in self.frames
            ],
        }


def _iter_frames(
    result: ResolutionResult,
) -> Iterator[RenderedFrame]:
    for index, resolved in enumerate(result.intervals):
        if not isinstance(resolved.interval, FrameInterval):  # defensive
            raise ResolutionError(
                f"resolved interval {index} is not a FrameInterval"
            )
        for frame in range(resolved.start, resolved.end):
            yield RenderedFrame(
                frame=frame,
                clip_id=resolved.clip_id,
                interval_index=index,
            )


def render_manifest(result: ResolutionResult) -> RenderManifest:
    """Expand a resolution result into a deterministic per-frame manifest."""
    frames = tuple(_iter_frames(result))

    # Internal consistency proof: frame count must equal post-resolution
    # coverage, and every referenced interval index must exist.
    expected = result.covered_frames_after
    if len(frames) != expected:
        raise ResolutionError(
            f"renderer produced {len(frames)} frames but resolved coverage "
            f"is {expected}"
        )
    for rendered in frames:
        if not 0 <= rendered.interval_index < len(result.intervals):
            raise ResolutionError(
                f"frame {rendered.frame} references missing interval index "
                f"{rendered.interval_index}"
            )
    return RenderManifest(
        fps=result.timeline.fps,
        frames=frames,
        intervals=result.intervals,
    )
