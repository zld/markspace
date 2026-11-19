"""Deterministic offline frame sampling over the timeline model.

The renderer never opens media or touches a graphics device.  For each sampled
timeline frame it picks the topmost video track (by ``level`` then id) that has
a clip covering that frame, then records the clip, its exact fade weight and
the mapped source frame.  The sampled track must already be free of overlaps:
the render path consumes the resolved model and refuses to invent a winner, so
timeline summaries and rendered frames cannot disagree.

Sampling is a linear sweep over pre-sorted clips, not a scan of all clips per
frame.

>>> seq = Sequence(id="s", frame_rate=FrameRate(24), tracks=(Track(
...     id="t", clips=(Clip(id="a", media_id="m", timeline_start=0,
...                          duration=4, source_in=0, source_duration=4),)),))
>>> render(seq).sample_count
4
"""

from __future__ import annotations

from dataclasses import dataclass
from fractions import Fraction
from typing import Any

from .errors import RenderError
from .model import Clip, Sequence, Track
from .timebase import require_int


@dataclass(frozen=True, slots=True)
class FrameSample:
    index: int
    track_id: str
    clip_id: str
    media_id: str
    source_frame: int
    fade_weight: Fraction

    def to_json(self) -> dict[str, Any]:
        return {
            "index": self.index,
            "track_id": self.track_id,
            "clip_id": self.clip_id,
            "media_id": self.media_id,
            "source_frame": self.source_frame,
            "fade_weight": [self.fade_weight.numerator, self.fade_weight.denominator],
        }


@dataclass(frozen=True, slots=True)
class RenderSummary:
    frame_rate: str
    step: int
    duration: int
    sample_count: int
    samples: tuple[FrameSample, ...]
    empty_frames: int

    @property
    def covered_count(self) -> int:
        return sum(1 for sample in self.samples if sample.clip_id)

    def to_json(self, *, include_samples: bool = True) -> dict[str, Any]:
        return {
            "frame_rate": self.frame_rate,
            "step": self.step,
            "duration": self.duration,
            "sample_count": self.sample_count,
            "covered_count": self.covered_count,
            "empty_frames": self.empty_frames,
            "samples": [sample.to_json() for sample in self.samples] if include_samples else [],
        }


def fade_weight(clip: Clip, local_frame: int) -> Fraction:
    """Exact linear fade gain at a clip-local frame.

    A fade-in of ``n`` frames ramps ``0/n, 1/n, ..., (n-1)/n``; a fade-out of
    ``n`` frames ramps ``(n-1)/n, ..., 0/n``.  Overlapping fades multiply.
    """

    require_int(local_frame, "local_frame", minimum=0)
    weight = Fraction(1)
    if clip.fade_in and local_frame < clip.fade_in:
        weight *= Fraction(local_frame, clip.fade_in - 1) if clip.fade_in > 1 else Fraction(0)
    remaining = clip.duration - 1 - local_frame
    if clip.fade_out and remaining < clip.fade_out:
        weight *= Fraction(remaining, clip.fade_out - 1) if clip.fade_out > 1 else Fraction(0)
    return weight


class _TrackCursor:
    """Forward-only pointer over a non-overlapping, start-sorted track."""

    __slots__ = ("track", "clips", "index")

    def __init__(self, track: Track) -> None:
        self.track = track
        self.clips = sorted(track.clips, key=lambda clip: (clip.timeline_start, clip.id))
        self.index = 0

    def clips_at(self, frame: int) -> list[Clip]:
        while self.index < len(self.clips) and self.clips[self.index].end <= frame:
            self.index += 1
        hits = []
        probe = self.index
        while probe < len(self.clips) and self.clips[probe].timeline_start <= frame:
            if frame < self.clips[probe].end:
                hits.append(self.clips[probe])
            probe += 1
        return hits


def render(sequence: Sequence, *, step: int = 1, start: int = 0,
           end: int | None = None) -> RenderSummary:
    """Sample timeline frames in ``[start, end)`` every ``step`` frames."""

    require_int(step, "step", minimum=1)
    require_int(start, "start", minimum=0)
    duration = sequence.duration if end is None else require_int(end, "end", minimum=0)
    if end is not None and end < start:
        raise RenderError(f"end {end} is before start {start}")

    video_tracks = [track for track in sequence.tracks if track.kind == "video"]
    if not video_tracks:
        raise RenderError("sequence has no video track to sample")
    # Highest level wins; ties break on the stable track id descending so that
    # stacking is deterministic regardless of document order.
    video_tracks.sort(key=lambda track: (track.level, track.id), reverse=True)
    cursors = [_TrackCursor(track) for track in video_tracks]

    samples: list[FrameSample] = []
    empty = 0
    for frame in range(start, duration, step):
        chosen: tuple[Track, Clip] | None = None
        for cursor in cursors:
            hits = cursor.clips_at(frame)
            if len(hits) > 1:
                raise RenderError(
                    f"frame {frame} is covered by {len(hits)} overlapping clips on "
                    f"track {cursor.track.id!r}; resolve conflicts before rendering",
                    track_id=cursor.track.id,
                    clip_id=hits[1].id,
                )
            if hits:
                chosen = (cursor.track, hits[0])
                break
        if chosen is None:
            empty += 1
            samples.append(FrameSample(frame, "", "", "", 0, Fraction(0)))
            continue
        track, clip = chosen
        local = frame - clip.timeline_start
        samples.append(FrameSample(
            index=frame,
            track_id=track.id,
            clip_id=clip.id,
            media_id=clip.media_id,
            source_frame=clip.source_in + clip.source_offset_at(local),
            fade_weight=fade_weight(clip, local),
        ))
    return RenderSummary(
        frame_rate=str(sequence.frame_rate),
        step=step,
        duration=duration,
        sample_count=len(samples),
        samples=tuple(samples),
        empty_frames=empty,
    )


def frame_signature(summary: RenderSummary) -> str:
    """Stable one-line signature of a render summary for determinism checks."""

    return "|".join(
        f"{s.index}:{s.track_id}:{s.clip_id}:{s.source_frame}:"
        f"{s.fade_weight.numerator}/{s.fade_weight.denominator}"
        for s in summary.samples
    )
