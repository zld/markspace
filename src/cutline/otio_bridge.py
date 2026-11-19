"""Map the internal model to and from OpenTimelineIO.

Two component-level modes are offered:

``serialize_clip`` / ``deserialize_clip``
    Lossless within OTIO: the exact model fields (including segmented speed
    mappings and fades) are stored in ``metadata["cutline"]`` while the clip
    also gets a real OTIO ``source_range``, ``ExternalReference`` and, for a
    single speed, a ``LinearTimeWarp`` effect.

``to_otio_clip``
    Structural export used by :func:`to_otio_track` and the CLI.  It raises
    :class:`BridgeError` for a segmented rate mapping, which a single OTIO clip
    cannot express.  Fades do not affect timing and are carried in
    ``metadata["cutline"]``.  A whole clip trimmed away by resolution is
    simply absent.

Round-trip verification checks clip order, time ranges and track kind, as
required by the acceptance criteria.
"""

from __future__ import annotations

from dataclasses import dataclass
from fractions import Fraction
from typing import Any

import opentimelineio as otio
from opentimelineio.schema import Clip as OtioClip
from opentimelineio.schema import ExternalReference, Gap, LinearTimeWarp, Track as OtioTrack

from .errors import BridgeError
from .model import Clip, RateSegment, Sequence, Track
from .timebase import FrameRate

_KIND_TO_OTIO = {"video": otio.schema.Track.Kind.Video, "audio": otio.schema.Track.Kind.Audio}
_OTIO_TO_KIND = {v: k for k, v in _KIND_TO_OTIO.items()}


@dataclass(frozen=True, slots=True)
class OTIOExportInfo:
    """Observable properties of a converted track, used for verification."""

    track_id: str
    kind: str
    clip_order: tuple[str, ...]
    clip_ranges: tuple[tuple[int, int], ...]

    def to_json(self) -> dict[str, Any]:
        return {
            "track_id": self.track_id,
            "kind": self.kind,
            "clip_order": list(self.clip_order),
            "clip_ranges": [list(pair) for pair in self.clip_ranges],
        }


def _time(value: int, rate: FrameRate) -> otio.opentime.RationalTime:
    # OTIO stores rates as doubles.  Frame counts stay exact; the exact
    # rational rate is echoed in timeline metadata by ``to_otio``.
    return otio.opentime.RationalTime(value, rate.fps)


def _range(start: int, duration: int, rate: FrameRate) -> otio.opentime.TimeRange:
    return otio.opentime.TimeRange(_time(start, rate), _time(duration, rate))


def check_exportable(clip: Clip) -> None:
    """Raise :class:`BridgeError` if ``clip`` has no faithful OTIO form."""

    if not clip.is_constant_speed:
        raise BridgeError(
            "segmented rate mapping cannot be expressed as a single OTIO clip; "
            "export segments as separate clips or use a constant speed",
            clip_id=clip.id,
        )


def to_otio_clip(clip: Clip, rate: FrameRate) -> OtioClip:
    check_exportable(clip)
    speed = clip.rate_segments[0].speed
    otio_clip = OtioClip(
        name=clip.id,
        media_reference=ExternalReference(target_url=clip.media_id),
        source_range=_range(clip.source_in, clip.source_frames, rate),
    )
    if speed != 1:
        otio_clip.effects.append(LinearTimeWarp(time_scalar=float(speed)))
    if clip.fade_in or clip.fade_out:
        otio_clip.metadata["cutline"] = {
            "clip_id": clip.id,
            "fade_in": clip.fade_in,
            "fade_out": clip.fade_out,
        }
    return otio_clip


def to_otio_track(track: Track, rate: FrameRate) -> OtioTrack:
    otio_track = OtioTrack(name=track.id, kind=_KIND_TO_OTIO[track.kind])
    cursor = 0
    for clip in sorted(track.clips, key=lambda c: (c.timeline_start, c.id)):
        if clip.timeline_start < cursor:
            raise BridgeError(
                f"clip starts at {clip.timeline_start} before cursor {cursor}; "
                "resolve overlaps before exporting",
                track_id=track.id,
                clip_id=clip.id,
            )
        if clip.timeline_start > cursor:
            gap = Gap()
            gap.source_range = _range(0, clip.timeline_start - cursor, rate)
            otio_track.append(gap)
        otio_track.append(to_otio_clip(clip, rate))
        cursor = clip.end
    return otio_track


def to_otio(sequence: Sequence) -> otio.schema.Timeline:
    timeline = otio.schema.Timeline(name=sequence.name)
    timeline.global_start_time = _time(0, sequence.frame_rate)
    timeline.metadata["cutline"] = {
        "sequence_id": sequence.id,
        "frame_rate": sequence.frame_rate.to_json(),
    }
    for track in sequence.tracks:
        timeline.tracks.append(to_otio_track(track, sequence.frame_rate))
    return timeline


def inspect_otio_track(otio_track: OtioTrack, rate: FrameRate) -> OTIOExportInfo:
    """Summarise an OTIO track in integer frames for verification."""

    names: list[str] = []
    ranges: list[tuple[int, int]] = []
    for item in otio_track:
        if isinstance(item, Gap):
            continue
        if not isinstance(item, OtioClip):
            raise BridgeError(f"unsupported OTIO item {type(item).__name__}")
        span = item.range_in_parent()
        names.append(item.name)
        ranges.append((int(round(span.start_time.value)), int(round(span.duration.value))))
    return OTIOExportInfo(
        track_id=otio_track.name,
        kind=_OTIO_TO_KIND.get(otio_track.kind, otio_track.kind),
        clip_order=tuple(names),
        clip_ranges=tuple(ranges),
    )


def verify_round_trip(sequence: Sequence, timeline: otio.schema.Timeline) -> list[OTIOExportInfo]:
    """Compare converted tracks against the model's clip order and time ranges."""

    expected_tracks = {track.id: track for track in sequence.tracks}
    seen: list[OTIOExportInfo] = []
    for otio_track in timeline.tracks:
        info = inspect_otio_track(otio_track, sequence.frame_rate)
        track = expected_tracks.get(info.track_id)
        if track is None:
            raise BridgeError(f"OTIO track {info.track_id!r} is not in the model")
        expected_order = tuple(
            clip.id for clip in sorted(track.clips, key=lambda c: (c.timeline_start, c.id))
        )
        expected_ranges = tuple(
            (clip.timeline_start, clip.duration)
            for clip in sorted(track.clips, key=lambda c: (c.timeline_start, c.id))
        )
        if info.clip_order != expected_order:
            raise BridgeError(
                f"clip order mismatch for track {track.id!r}: "
                f"OTIO {info.clip_order} != model {expected_order}",
                track_id=track.id,
            )
        if info.clip_ranges != expected_ranges:
            raise BridgeError(
                f"time range mismatch for track {track.id!r}: "
                f"OTIO {info.clip_ranges} != model {expected_ranges}",
                track_id=track.id,
            )
        probe = next((item for item in otio_track if isinstance(item, OtioClip)), None)
        otio_rate = probe.source_range.start_time.rate if probe is not None else None
        if otio_rate is not None and abs(otio_rate - sequence.frame_rate.fps) > 1e-6:
            raise BridgeError(
                f"frame rate mismatch for track {track.id!r}: "
                f"OTIO {otio_rate} != model {sequence.frame_rate}"
            )
        if info.kind != track.kind:
            raise BridgeError(
                f"track kind mismatch for {track.id!r}: OTIO {info.kind} != model {track.kind}",
                track_id=track.id,
            )
        seen.append(info)
    if len(seen) != len(sequence.tracks):
        raise BridgeError(
            f"OTIO timeline has {len(seen)} tracks but model has {len(sequence.tracks)}"
        )
    return seen


# --------------------------------------------------------------------------
# Lossless component mode


def _speed_doc(segment: RateSegment) -> dict[str, int]:
    return {"offset": segment.offset, "duration": segment.duration,
            "numerator": segment.speed.numerator, "denominator": segment.speed.denominator}


def serialize_clip(clip: Clip, rate: FrameRate) -> OtioClip:
    """Lossless export: real OTIO timing plus exact model fields in metadata."""

    otio_clip = OtioClip(
        name=clip.id,
        media_reference=ExternalReference(target_url=clip.media_id),
        source_range=_range(clip.source_in, clip.source_frames, rate),
    )
    otio_clip.metadata["cutline"] = {
        "id": clip.id,
        "timeline_start": clip.timeline_start,
        "duration": clip.duration,
        "source_in": clip.source_in,
        "source_duration": clip.source_duration,
        "fade_in": clip.fade_in,
        "fade_out": clip.fade_out,
        "priority": clip.priority,
        "rate_segments": [_speed_doc(segment) for segment in clip.rate_segments],
    }
    return otio_clip


def deserialize_clip(otio_clip: OtioClip, rate: FrameRate) -> Clip:
    data = otio_clip.metadata.get("cutline")
    if not data:
        raise BridgeError(
            f"OTIO clip {otio_clip.name!r} has no 'cutline' metadata; cannot restore exact model",
            clip_id=otio_clip.name,
        )
    try:
        segments = tuple(
            RateSegment(
                offset=int(item["offset"]),
                duration=int(item["duration"]),
                speed=Fraction(int(item["numerator"]), int(item["denominator"])),
            )
            for item in data["rate_segments"]
        )
        return Clip(
            id=str(data["id"]),
            media_id=str(otio_clip.media_reference.target_url),
            timeline_start=int(data["timeline_start"]),
            duration=int(data["duration"]),
            source_in=int(data["source_in"]),
            source_duration=int(data["source_duration"]),
            rate_segments=segments,
            fade_in=int(data["fade_in"]),
            fade_out=int(data["fade_out"]),
            priority=int(data["priority"]),
        )
    except (KeyError, TypeError, ValueError) as exc:
        raise BridgeError(
            f"malformed cutline metadata on OTIO clip {otio_clip.name!r}: {exc}",
            clip_id=otio_clip.name,
        ) from exc


def serialize_clip_json(clip: Clip, rate: FrameRate) -> str:
    return otio.adapters.write_to_string(serialize_clip(clip, rate), "otio_json")


def deserialize_clip_json(text: str, rate: FrameRate) -> Clip:
    try:
        otio_clip = otio.adapters.read_from_string(text, "otio_json")
    except Exception as exc:  # OTIO raises its own error type
        raise BridgeError(f"cannot parse OTIO JSON: {exc}") from exc
    if not isinstance(otio_clip, OtioClip):
        raise BridgeError("OTIO JSON does not contain a clip")
    return deserialize_clip(otio_clip, rate)
