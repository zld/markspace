"""Adapter between the cutline model and OpenTimelineIO objects.

OTIO tracks are sequential stacks, so explicit start times are encoded
by inserting Gap children between clips.  Exact rational values that
OTIO cannot represent (its RationalTime stores floats) are preserved in
``metadata["cutline"]`` so a round trip is lossless.  Unsupported
objects or missing time ranges raise AdapterError naming the object.
"""

from __future__ import annotations

from fractions import Fraction
from typing import Any

import opentimelineio as otio

from .model import Clip, Gap, MediaRef, Timeline, Track

META_KEY = "cutline"


class AdapterError(ValueError):
    """Raised when an OTIO object cannot be converted."""


def _rt(frames: Fraction, rate: Fraction) -> otio.opentime.RationalTime:
    return otio.opentime.RationalTime(float(frames), float(rate))


def _time_range(start: Fraction, duration: Fraction, rate: Fraction) -> otio.opentime.TimeRange:
    return otio.opentime.TimeRange(start_time=_rt(start, rate),
                                   duration=_rt(duration, rate))


def _frac_meta(value: Fraction) -> Any:
    return value.numerator if value.denominator == 1 else [value.numerator, value.denominator]


def _frac_from_meta(value: Any) -> Fraction:
    if isinstance(value, list):
        return Fraction(value[0], value[1])
    return Fraction(value)


def to_otio(timeline: Timeline) -> otio.schema.Timeline:
    result = otio.schema.Timeline(name=timeline.name)
    result.metadata[META_KEY] = {
        "rate": _frac_meta(timeline.rate),
        "version": 1,
    }
    for track in timeline.tracks:
        ot_track = otio.schema.Track(
            name=track.name,
            kind=(otio.schema.TrackKind.Video if track.kind == "video"
                  else otio.schema.TrackKind.Audio),
        )
        ot_track.metadata[META_KEY] = {"mode": track.mode}
        cursor = Fraction(0)
        for item in sorted(track.items, key=lambda it: it.start):
            if item.start > cursor:
                ot_track.append(otio.schema.Gap(
                    source_range=_time_range(cursor, item.start - cursor, timeline.rate)))
            elif item.start < cursor:
                raise AdapterError(
                    f"track {track.name!r}: item at {item.start} overlaps previous "
                    f"content ending at {cursor}; resolve conflicts before OTIO export"
                )
            if isinstance(item, Gap):
                ot_track.append(otio.schema.Gap(
                    source_range=_time_range(item.start, item.duration, timeline.rate)))
            else:
                ot_clip = otio.schema.Clip(name=item.name)
                ot_clip.source_range = _time_range(
                    item.source_in, item.source_out - item.source_in, item.media.rate)
                available = (_time_range(Fraction(0), item.media.duration, item.media.rate)
                             if item.media.duration is not None else None)
                ot_clip.media_reference = otio.schema.ExternalReference(
                    target_url=item.media.uri or item.media.name,
                    available_range=available,
                )
                ot_clip.metadata[META_KEY] = {
                    "clip_id": item.clip_id,
                    "priority": item.priority,
                    "start": _frac_meta(item.start),
                    "duration": _frac_meta(item.duration),
                    "source_in": _frac_meta(item.source_in),
                    "source_out": _frac_meta(item.source_out),
                    "media_rate": _frac_meta(item.media.rate),
                    "media_duration": (_frac_meta(item.media.duration)
                                       if item.media.duration is not None else None),
                    "media_name": item.media.name,
                }
                ot_track.append(ot_clip)
            cursor = item.end
        result.tracks.append(ot_track)
    return result


def _frames_at(rt: otio.opentime.RationalTime, rate: Fraction, ctx: str) -> Fraction:
    return Fraction(rt.value) * rate / Fraction(rt.rate)


def from_otio(timeline: otio.schema.Timeline) -> Timeline:
    meta = timeline.metadata.get(META_KEY, {})
    if "rate" not in meta:
        raise AdapterError(
            f"timeline {timeline.name!r}: missing cutline rate metadata; "
            f"cannot determine project frame rate"
        )
    rate = _frac_from_meta(meta["rate"])
    tracks: list[Track] = []
    for ti, ot_track in enumerate(timeline.tracks):
        if not isinstance(ot_track, otio.schema.Track):
            raise AdapterError(
                f"timeline {timeline.name!r} child[{ti}]: unsupported object "
                f"{type(ot_track).__name__} {getattr(ot_track, 'name', '')!r}"
            )
        tmeta = ot_track.metadata.get(META_KEY, {})
        mode = tmeta.get("mode", "exclusive")
        kind = "video" if ot_track.kind == otio.schema.TrackKind.Video else "audio"
        items: list[Any] = []
        cursor = Fraction(0)
        for ci, child in enumerate(ot_track):
            ctx = f"track {ot_track.name!r} child[{ci}]"
            if isinstance(child, otio.schema.Gap):
                rng = child.source_range()
                dur = (_frames_at(rng.duration(), rate, ctx) if rng is not None
                       else Fraction(0))
                if dur > 0:
                    items.append(Gap(start=cursor, duration=dur))
                cursor += dur
            elif isinstance(child, otio.schema.Clip):
                cmeta = child.metadata.get(META_KEY, {})
                rng = child.source_range()
                if rng is None and not cmeta:
                    raise AdapterError(
                        f"{ctx}: clip {child.name!r} has no source range"
                    )
                if cmeta:
                    start = _frac_from_meta(cmeta["start"])
                    dur = _frac_from_meta(cmeta["duration"])
                    source_in = _frac_from_meta(cmeta["source_in"])
                    source_out = _frac_from_meta(cmeta["source_out"])
                    media_rate = _frac_from_meta(cmeta["media_rate"])
                    media_dur = (_frac_from_meta(cmeta["media_duration"])
                                 if cmeta.get("media_duration") is not None else None)
                    media_name = cmeta.get("media_name", child.name)
                    clip_id = cmeta["clip_id"]
                    priority = int(cmeta.get("priority", 0))
                else:
                    start = cursor
                    dur = _frames_at(rng.duration(), rate, ctx)
                    source_in = _frames_at(rng.start_time(), rate, ctx)
                    source_out = source_in + dur
                    media_rate = rate
                    media_dur = None
                    media_name = child.name
                    clip_id = f"{ot_track.name}:{ci}"
                    priority = 0
                ref = child.media_reference
                uri = getattr(ref, "target_url", None) if ref is not None else None
                items.append(Clip(
                    clip_id=clip_id,
                    name=child.name,
                    start=start,
                    duration=dur,
                    source_in=source_in,
                    source_out=source_out,
                    media=MediaRef(name=media_name, uri=uri,
                                   rate=media_rate, duration=media_dur),
                    priority=priority,
                ))
                cursor = start + dur
            else:
                raise AdapterError(
                    f"{ctx}: unsupported object {type(child).__name__} "
                    f"{getattr(child, 'name', '')!r}"
                )
        tracks.append(Track(name=ot_track.name or f"track{ti}", kind=kind,
                            mode=mode, items=items))
    return Timeline(name=timeline.name or "timeline", rate=rate, tracks=tracks)
