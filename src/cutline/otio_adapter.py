"""Adapter between the cutline model and OpenTimelineIO objects.

OTIO tracks are sequential stacks, so explicit start times are encoded
by inserting Gap children between clips.  Layered tracks, whose clips
may overlap, are decomposed into non-overlapping lanes (interval graph
coloring); each lane becomes an OTIO track carrying ``group``/``lane``
metadata so ``from_otio`` can merge them back.  Exact rational values
that OTIO cannot represent (its RationalTime stores floats) are
preserved in ``metadata["cutline"]`` so a round trip is lossless.
Unsupported objects or missing time ranges raise AdapterError naming
the object.
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
    if isinstance(value, bool):
        raise AdapterError(f"cannot decode rational from metadata {value!r}")
    if isinstance(value, int):
        return Fraction(value)
    # OTIO may store sequences as its own AnyVector type, not list.
    try:
        seq = list(value)
    except TypeError as exc:
        raise AdapterError(f"cannot decode rational from metadata {value!r}") from exc
    if len(seq) == 2 and all(isinstance(v, int) and not isinstance(v, bool) for v in seq):
        return Fraction(seq[0], seq[1])
    raise AdapterError(f"cannot decode rational from metadata {value!r}")


def _fill_track(ot_track: "otio.schema.Track", items: list[Any],
                rate: Fraction, track_name: str) -> None:
    cursor = Fraction(0)
    for item in sorted(items, key=lambda it: it.start):
        if item.start > cursor:
            ot_track.append(otio.schema.Gap(
                source_range=_time_range(cursor, item.start - cursor, rate)))
        elif item.start < cursor:
            raise AdapterError(
                f"track {track_name!r}: item at {item.start} overlaps previous "
                f"content ending at {cursor}; resolve conflicts before OTIO export"
            )
        if isinstance(item, Gap):
            ot_gap = otio.schema.Gap(
                source_range=_time_range(item.start, item.duration, rate))
            ot_gap.metadata[META_KEY] = {"explicit": True}
            ot_track.append(ot_gap)
        else:
            ot_track.append(_clip_to_otio(item))
        cursor = item.end


def _clip_to_otio(item: Clip) -> "otio.schema.Clip":
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
        "media_uri": item.media.uri if item.media.uri is not None else "",
    }
    return ot_clip


def _lanes(items: list[Any]) -> list[list[Any]]:
    """Assign items to the fewest non-overlapping lanes (greedy, stable)."""
    lanes: list[list[Any]] = []
    lane_ends: list[Fraction] = []
    for item in sorted(items, key=lambda it: (it.start, getattr(it, "clip_id", ""))):
        for li, end in enumerate(lane_ends):
            if item.start >= end:
                lanes[li].append(item)
                lane_ends[li] = item.end
                break
        else:
            lanes.append([item])
            lane_ends.append(item.end)
    return lanes


def to_otio(timeline: Timeline) -> otio.schema.Timeline:
    result = otio.schema.Timeline(name=timeline.name)
    result.metadata[META_KEY] = {
        "rate": _frac_meta(timeline.rate),
        "version": 1,
    }
    for track in timeline.tracks:
        kind = (otio.schema.TrackKind.Video if track.kind == "video"
                else otio.schema.TrackKind.Audio)
        lanes = _lanes(track.items) if track.mode == "layered" else [track.items]
        for lane_idx, lane_items in enumerate(lanes):
            name = track.name if lane_idx == 0 else f"{track.name}#{lane_idx}"
            ot_track = otio.schema.Track(name=name, kind=kind)
            ot_track.metadata[META_KEY] = {
                "mode": track.mode,
                "group": track.name,
                "lane": lane_idx,
            }
            _fill_track(ot_track, lane_items, timeline.rate, track.name)
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
    groups: dict[str, dict[str, Any]] = {}
    order: list[str] = []
    for ti, ot_track in enumerate(timeline.tracks):
        if not isinstance(ot_track, otio.schema.Track):
            raise AdapterError(
                f"timeline {timeline.name!r} child[{ti}]: unsupported object "
                f"{type(ot_track).__name__} {getattr(ot_track, 'name', '')!r}"
            )
        tmeta = ot_track.metadata.get(META_KEY, {})
        mode = tmeta.get("mode", "exclusive")
        group = tmeta.get("group", ot_track.name or f"track{ti}")
        kind = "video" if ot_track.kind == otio.schema.TrackKind.Video else "audio"
        if group not in groups:
            groups[group] = {"mode": mode, "kind": kind, "items": []}
            order.append(group)
        items: list[Any] = groups[group]["items"]
        cursor = Fraction(0)
        for ci, child in enumerate(ot_track):
            ctx = f"track {ot_track.name!r} child[{ci}]"
            if isinstance(child, otio.schema.Gap):
                rng = child.source_range
                dur = (_frames_at(rng.duration, rate, ctx) if rng is not None
                       else Fraction(0))
                # Only gaps the model explicitly declared round-trip as Gap
                # items; the rest are spacing inserted by _fill_track.
                if dur > 0 and child.metadata.get(META_KEY, {}).get("explicit"):
                    items.append(Gap(start=cursor, duration=dur))
                cursor += dur
            elif isinstance(child, otio.schema.Clip):
                cmeta = child.metadata.get(META_KEY, {})
                rng = child.source_range
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
                    dur = _frames_at(rng.duration, rate, ctx)
                    source_in = _frames_at(rng.start_time, rate, ctx)
                    source_out = source_in + dur
                    media_rate = rate
                    media_dur = None
                    media_name = child.name
                    clip_id = f"{ot_track.name}:{ci}"
                    priority = 0
                ref = child.media_reference
                uri = getattr(ref, "target_url", None) if ref is not None else None
                if cmeta:
                    meta_uri = cmeta.get("media_uri", "")
                    uri = meta_uri if meta_uri else None
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
    tracks = [
        Track(name=group, kind=groups[group]["kind"],
              mode=groups[group]["mode"],
              items=sorted(groups[group]["items"],
                           key=lambda it: (it.start, getattr(it, "clip_id", ""))))
        for group in order
    ]
    return Timeline(name=timeline.name or "timeline", rate=rate, tracks=tracks)
