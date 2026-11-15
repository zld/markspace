"""Map the internal model onto an OpenTimelineIO timeline.

The internal rational model remains the single source of truth; OTIO
objects are built on demand purely for export. Any mapping or
serialization failure is reported with the offending clip id and raised
as :class:`ExportError` (process exit code 4).
"""
from __future__ import annotations

import opentimelineio as otio

from .errors import ExportError
from .model import Timeline, frames


def _time_range(start_frames: int, duration_frames: int, rate: float):
    return otio.opentime.TimeRange(
        start_time=otio.opentime.RationalTime(start_frames, rate),
        duration=otio.opentime.RationalTime(duration_frames, rate),
    )


def _build_clip(clip, fps, rate):
    try:
        start_frames = frames(clip.start, fps)
        duration_frames = frames(clip.duration, fps)
        source_frames = frames(clip.source_start, fps)
        otio_clip = otio.schema.Clip(
            name=clip.id,
            media_reference=otio.schema.MissingReference(),
            source_range=_time_range(source_frames, duration_frames, rate),
        )
        if clip.metadata:
            otio_clip.metadata["clipweave"] = dict(clip.metadata)
        return start_frames, duration_frames, otio_clip
    except ExportError:
        raise
    except Exception as exc:
        raise ExportError(
            f"failed to map clip to OTIO: {exc}", clip_id=clip.id
        ) from exc


def to_otio(timeline: Timeline):
    """Build an OTIO timeline from the model (model stays the truth)."""
    rate = float(timeline.fps)
    if rate <= 0:
        raise ExportError(f"invalid frame rate for OTIO export: {timeline.fps}")
    otio_timeline = otio.schema.Timeline(name=timeline.name)
    for track in timeline.tracks:
        otio_track = otio.schema.Track(
            name=track.name, kind=otio.schema.TrackKind.Video
        )
        cursor = 0  # frames placed so far on this track
        for clip in sorted(track.clips, key=lambda c: (c.start, c.id)):
            start_frames, duration_frames, otio_clip = _build_clip(
                clip, timeline.fps, rate
            )
            if start_frames < cursor:
                raise ExportError(
                    "clip overlaps a predecessor or starts before frame 0; "
                    "resolve the timeline before export",
                    clip_id=clip.id,
                )
            if start_frames > cursor:
                otio_track.append(
                    otio.schema.Gap(
                        source_range=_time_range(0, start_frames - cursor, rate)
                    )
                )
            otio_track.append(otio_clip)
            cursor = start_frames + duration_frames
        otio_timeline.tracks.append(otio_track)
    return otio_timeline


def write_otio(timeline: Timeline, path: str) -> None:
    """Serialize the model to ``path`` via the OTIO adapter for its suffix."""
    otio_timeline = to_otio(timeline)
    try:
        otio.adapters.write_to_file(otio_timeline, path)
    except Exception as exc:
        raise ExportError(f"OTIO serialization to {path} failed: {exc}") from exc
