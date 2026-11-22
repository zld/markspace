"""Conversion between the canonical model and OpenTimelineIO objects.

Model -> OTIO:

* one :class:`otio.schema.Track` per model track (kind ``Video``);
* clips keep their absolute frame position; gaps are inserted automatically
  because OTIO lays track children out sequentially;
* identity fields live under ``metadata["timeline_core"]`` so the
  round-trip is exact (``clip_id``, ``priority``, frame positions).

OTIO -> model:

* frame positions are derived from OTIO geometry
  (``Track.range_of_child`` plus the timeline global start), not from
  metadata, so hand-authored OTIO imports correctly;
* :class:`otio.schema.Gap` children are skipped;
* every RationalTime rate must be a positive integer, matching the model's
  integer frame scale.
"""

from __future__ import annotations

from typing import Any

import opentimelineio as otio
from opentimelineio import opentime as _ot

from .model import (
    Clip,
    FrameInterval,
    ResolutionError,
    Timeline,
    Track,
)

METADATA_NAMESPACE = "timeline_core"


def _integer_rate(rate: float, context: str) -> int:
    if rate is None or rate <= 0:
        raise ResolutionError(f"{context} must have a positive rate, got {rate!r}")
    rounded = round(rate)
    if abs(rate - rounded) > 1e-9:
        raise ResolutionError(
            f"integer frame scale requires an integer frame rate; "
            f"{context} has rate {rate}"
        )
    return rounded


def to_otio(timeline: Timeline) -> otio.schema.Timeline:
    """Build an OTIO timeline whose geometry matches the canonical model."""
    fps = timeline.fps
    otio_timeline = otio.schema.Timeline(
        name="timeline_core",
        global_start_time=_ot.RationalTime(0, fps),
    )
    for track in timeline.tracks:
        otio_track = otio.schema.Track(name=track.name, kind=otio.schema.TrackKind.Video)
        cursor = 0
        for clip in sorted(track.clips, key=lambda c: c.interval.start):
            start = clip.interval.start
            if start < cursor:
                raise ResolutionError(
                    f"track {track.name!r} clips overlap within the track; "
                    "run the resolver before OTIO export"
                )
            if start > cursor:
                otio_track.append(
                    otio.schema.Gap(
                        source_range=otio.opentime.TimeRange(
                            start_time=_ot.RationalTime(0, fps),
                            duration=_ot.RationalTime(start - cursor, fps),
                        )
                    )
                )
            duration = clip.interval.duration_frames
            otio_clip = otio.schema.Clip(
                name=clip.clip_id,
                source_range=otio.opentime.TimeRange(
                    start_time=_ot.RationalTime(0, fps),
                    duration=_ot.RationalTime(duration, fps),
                ),
            )
            otio_clip.metadata[METADATA_NAMESPACE] = {
                "clip_id": clip.clip_id,
                "priority": clip.priority,
                "start_frame": clip.interval.start,
                "end_frame": clip.interval.end,
                "track": clip.track,
            }
            otio_track.append(otio_clip)
            cursor = clip.interval.end
        otio_timeline.tracks.append(otio_track)
    return otio_timeline


def _resolve_fps(otio_timeline: otio.schema.Timeline) -> tuple[int, int]:
    """Return (fps, global start frame) from timeline/clip RationalTimes."""
    global_start_frame = 0
    fps: int | None = None
    global_start = otio_timeline.global_start_time
    if global_start is not None:
        fps = _integer_rate(global_start.rate, "timeline global_start_time")
        global_start_frame = _ot.to_frames(global_start, fps)
    for track in otio_timeline.tracks:
        for child in track:
            if isinstance(child, otio.schema.Gap):
                continue
            child_range = child.trimmed_range()
            child_fps = _integer_rate(
                child_range.start_time.rate, f"clip {child.name!r}"
            )
            if fps is None:
                fps = child_fps
            elif child_fps != fps:
                raise ResolutionError(
                    f"mixed frame rates in OTIO timeline: {fps} vs {child_fps} "
                    f"on clip {child.name!r}"
                )
    if fps is None:
        raise ResolutionError("OTIO timeline contains no clips to derive a rate")
    return fps, global_start_frame


def from_otio(otio_timeline: otio.schema.Timeline) -> Timeline:
    """Convert an OTIO timeline into the canonical :class:`Timeline`."""
    fps, global_start_frame = _resolve_fps(otio_timeline)
    tracks: list[Track] = []
    for otio_track in otio_timeline.tracks:
        clips: list[Clip] = []
        for child in otio_track:
            if isinstance(child, otio.schema.Gap):
                continue
            if not isinstance(child, otio.schema.Clip):
                raise ResolutionError(
                    f"unsupported OTIO item {type(child).__name__} "
                    f"in track {otio_track.name!r}"
                )
            local_range = otio_track.range_of_child(child)
            start = global_start_frame + _ot.to_frames(local_range.start_time, fps)
            duration = _ot.to_frames(local_range.duration, fps)
            meta: dict[str, Any] = dict(
                child.metadata.get(METADATA_NAMESPACE, {})
            )
            clip_id = meta.get("clip_id", child.name)
            if not isinstance(clip_id, str) or not clip_id:
                raise ResolutionError(
                    f"clip in track {otio_track.name!r} has no usable id"
                )
            priority = meta.get("priority", 0)
            if not isinstance(priority, int) or isinstance(priority, bool):
                raise ResolutionError(
                    f"clip {clip_id!r}: priority metadata must be an integer"
                )
            clips.append(
                Clip(
                    clip_id=clip_id,
                    interval=FrameInterval(start, start + duration),
                    priority=priority,
                    track=otio_track.name,
                )
            )
        tracks.append(Track.from_clips(otio_track.name, clips))
    timeline = Timeline(fps, tuple(tracks))
    timeline.validate_unique_ids()
    return timeline


def write_otio_file(timeline: Timeline, path: str) -> None:
    otio.adapters.write_to_file(to_otio(timeline), path)


def read_otio_file(path: str) -> Timeline:
    return from_otio(otio.adapters.read_from_file(path))
