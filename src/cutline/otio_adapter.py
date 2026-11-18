"""Conversion between the native model and OpenTimelineIO 0.17 objects.

Mapping rules
-------------
* Track order is preserved exactly.
* Clip ids are stored in ``metadata["cutline"]["id"]`` so they survive even
  when OTIO normalises names.
* Times are stored as ``otio.opentime.RationalTime`` at the clip's own frame
  rate, rounded half away from zero.
* The native model allows explicit timeline positions (gaps and overlaps),
  which a plain OTIO track expresses by sequencing its children.  The exact
  rational start/duration are therefore also stored in the clip metadata;
  import prefers those values, so an export/import round trip restores the
  original timeline to the second.  Without the metadata, import reads the
  source range in track order, which is what a foreign OTIO file means.
* An OTIO object that cannot be mapped (stack, effect, transition, or a
  non-timeline root) raises :class:`OTIOUnmappedError` carrying the object's
  path such as ``tracks[1].children[3]``.
"""

from __future__ import annotations

from collections.abc import Mapping
from fractions import Fraction
from typing import Any

import opentimelineio as otio

from .rational import (
    RationalTime,
    TimeError,
    format_fraction,
    parse_rate,
    parse_seconds,
    rate_from_float,
)

METADATA_KEY = "cutline"


class OTIOUnmappedError(ValueError):
    def __init__(self, path: str, object_type: str, message: str) -> None:
        self.path = path
        self.object_type = object_type
        super().__init__(f"{path}: cannot map OTIO {object_type}: {message}")


def _frames_as_seconds(frames: float, rate: Fraction) -> Fraction:
    return Fraction(int(round(frames))) / rate if rate else Fraction(0)


def _meta(obj: Any) -> dict[str, Any]:
    # OTIO returns AnyDictionary, which is a Mapping but not a dict subclass.
    value = obj.metadata.get(METADATA_KEY)
    return dict(value) if isinstance(value, Mapping) else {}


def to_otio(timeline: Any) -> otio.schema.Timeline:
    """Build an OTIO timeline mirroring the native model."""
    otio_tracks = []
    for track in timeline.tracks:
        children = []
        for clip in track.clips:
            rate = clip.rate
            start_frames = clip.start.to_frames(rate)
            duration_frames = clip.duration.to_frames(rate)
            if duration_frames <= 0:
                # Sub-frame clips are not representable in OTIO integers; keep
                # one frame so the round trip never silently loses a clip.
                duration_frames = 1
            otio_clip = otio.schema.Clip(
                name=clip.name or clip.id,
                source_range=otio.opentime.TimeRange(
                    start_time=otio.opentime.RationalTime(start_frames, float(rate)),
                    duration=otio.opentime.RationalTime(duration_frames, float(rate)),
                ),
            )
            otio_clip.metadata[METADATA_KEY] = {
                "id": clip.id,
                "priority": clip.priority,
                "start": clip.start.as_seconds_text(),
                "duration": clip.duration.as_seconds_text(),
                "rate": format_fraction(clip.rate),
                **({"clip_metadata": dict(clip.metadata)} if clip.metadata else {}),
            }
            children.append(otio_clip)
        otio_track = otio.schema.Track(
            name=track.name or track.id,
            kind=otio.schema.TrackKind.Video,
            children=children,
        )
        otio_track.metadata[METADATA_KEY] = {
            "id": track.id,
            "rate": format_fraction(track.rate),
            **({"track_metadata": dict(track.metadata)} if track.metadata else {}),
        }
        otio_tracks.append(otio_track)

    result = otio.schema.Timeline(name=timeline.name or timeline.id)
    result.tracks.extend(otio_tracks)
    result.metadata[METADATA_KEY] = {
        "id": timeline.id,
        **({"timeline_metadata": dict(timeline.metadata)} if timeline.metadata else {}),
    }
    return result


def _clip_rate(item: Any, meta: dict[str, Any], path: str) -> Fraction:
    if "rate" in meta:
        try:
            return parse_rate(meta["rate"], f"{path}.rate")
        except TimeError as exc:
            raise OTIOUnmappedError(path, "Clip", str(exc)) from None
    source = item.source_range
    if source is None:
        raise OTIOUnmappedError(path, "Clip", "missing source_range")
    try:
        return rate_from_float(source.start_time.rate, f"{path}.rate")
    except TimeError as exc:
        raise OTIOUnmappedError(path, "Clip", str(exc)) from None


def from_otio(root: Any) -> Any:
    """Read an OTIO timeline (or timeline-like stack) into the native model."""
    from .model import Clip, Timeline, Track

    if isinstance(root, otio.schema.Timeline):
        stack = root.tracks
        timeline_name = root.name or "timeline"
        timeline_meta = _meta(root)
    elif isinstance(root, otio.schema.Stack):
        stack = root
        timeline_name = root.name or "timeline"
        timeline_meta = _meta(root)
    else:
        raise OTIOUnmappedError("<root>", type(root).__name__, "expected Timeline or Stack")
    timeline_id = timeline_meta.get("id", "timeline")

    tracks: list[Track] = []
    for t_index, child in enumerate(stack):
        path = f"tracks[{t_index}]"
        if not isinstance(child, otio.schema.Track):
            raise OTIOUnmappedError(path, type(child).__name__, "expected a Track")
        meta = _meta(child)
        track_id = meta.get("id") or f"track-{t_index}"
        try:
            rate = (
                parse_rate(meta["rate"], f"{path}.rate")
                if "rate" in meta
                else Fraction(24)
            )
        except TimeError as exc:
            raise OTIOUnmappedError(path, "Track", str(exc)) from None
        clips: list[Clip] = []
        cursor = Fraction(0)  # OTIO tracks are sequential: positions accumulate.
        for c_index, item in enumerate(child):
            c_path = f"{path}.children[{c_index}]"
            if isinstance(item, otio.schema.Gap):
                if item.source_range is not None:
                    cursor += _frames_as_seconds(item.source_range.duration.to_frames(), rate)
                continue
            if not isinstance(item, otio.schema.Clip):
                raise OTIOUnmappedError(c_path, type(item).__name__, "expected a Clip or Gap")
            clip_meta = _meta(item)
            clip_id = clip_meta.get("id") or f"{track_id}-clip-{c_index}"
            clip_rate = _clip_rate(item, clip_meta, c_path)
            source = item.source_range
            if source is None:
                raise OTIOUnmappedError(c_path, "Clip", "missing source_range")
            try:
                if "start" in clip_meta and "duration" in clip_meta:
                    start = RationalTime(parse_seconds(clip_meta["start"], c_path))
                    duration = RationalTime(parse_seconds(clip_meta["duration"], c_path))
                else:
                    start = RationalTime.from_frames(int(source.start_time.to_frames()), clip_rate)
                    duration = RationalTime.from_frames(int(source.duration.to_frames()), clip_rate)
            except (TimeError, ValueError) as exc:
                raise OTIOUnmappedError(c_path, "Clip", str(exc)) from None
            if "start" not in clip_meta:
                # Foreign OTIO file: sequence position, not source in-point.
                start = RationalTime(cursor)
            cursor = start.seconds + duration.seconds
            clips.append(
                Clip(
                    id=str(clip_id),
                    name=item.name or None,
                    start=start,
                    duration=duration,
                    rate=clip_rate,
                    priority=int(clip_meta.get("priority", 0)),
                    metadata=dict(clip_meta.get("clip_metadata") or {}),
                )
            )
        tracks.append(
            Track(
                id=str(track_id),
                name=child.name or None,
                rate=rate,
                metadata=dict(meta.get("track_metadata") or {}),
                clips=clips,
            )
        )

    return Timeline(
        id=str(timeline_id),
        name=timeline_name,
        tracks=tracks,
        metadata=dict(timeline_meta.get("timeline_metadata") or {}),
    )
