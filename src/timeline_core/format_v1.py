"""First-version JSON exchange format.

Layout::

    {
      "version": 1,
      "fps": 24,
      "tracks": [
        {"name": "V1",
         "clips": [
           {"id": "A", "start_seconds": 0, "end_seconds": 5,
            "priority": 1}
         ]}
      ]
    }

A flat top-level ``clips`` array (no tracks) is accepted on read and placed
on a ``default`` track. Times may be integers, decimals or exact fraction
strings such as ``"1/48"``; conversion to frames happens in the model layer.
"""

from __future__ import annotations

from fractions import Fraction
from pathlib import Path
from typing import Any

from .format_common import (
    as_fraction,
    check_version,
    dump_json,
    load_json,
    require_field,
    require_int,
    require_object,
)
from .model import (
    Clip,
    FrameInterval,
    ResolutionError,
    Timeline,
    Track,
    frames_from_seconds,
)

VERSION = 1


def format_fraction(value: Fraction) -> int | str:
    """Render an exact fraction as int, finite decimal or fraction string."""
    if value.denominator == 1:
        return value.numerator
    # Finite decimal when the denominator has no prime factors other than 2,5.
    remaining = value.denominator
    tens = 0
    while remaining % 2 == 0:
        remaining //= 2
        tens += 1
    fives = 0
    while remaining % 5 == 0:
        remaining //= 5
        fives += 1
    if remaining == 1:
        places = max(tens, fives)
        scaled = value.numerator * 10**places // value.denominator
        text = str(scaled).rjust(places + 1, "0")
        return text[:-places] + "." + text[-places:]
    return f"{value.numerator}/{value.denominator}"


def _parse_clip(raw: Any, fps: int, track_name: str) -> Clip:
    obj = require_object(raw)
    context = f"clip in track {track_name!r}"
    clip_id = require_field(obj, "id", context)
    if not isinstance(clip_id, str) or not clip_id:
        raise ResolutionError(f"clip id must be a non-empty string, got {clip_id!r}")
    start = frames_from_seconds(
        as_fraction(require_field(obj, "start_seconds", context), "start_seconds"),
        fps,
    )
    end = frames_from_seconds(
        as_fraction(require_field(obj, "end_seconds", context), "end_seconds"),
        fps,
    )
    if end < start:
        raise ResolutionError(
            f"clip {clip_id!r} has negative duration: "
            f"start_seconds maps to frame {start}, end_seconds to {end}"
        )
    priority = require_int(
        require_field(obj, "priority", context), "priority"
    )
    return Clip(
        clip_id=clip_id,
        interval=FrameInterval(start, end),
        priority=priority,
        track=track_name,
    )


def _parse_tracks(data: dict[str, Any], fps: int) -> list[Track]:
    if "tracks" in data:
        raw_tracks = require_field(data, "tracks", "document")
        if not isinstance(raw_tracks, list):
            raise ResolutionError("field 'tracks' must be an array")
        tracks: list[Track] = []
        for index, raw_track in enumerate(raw_tracks):
            track_obj = require_object(raw_track)
            name = require_field(track_obj, "name", f"tracks[{index}]")
            if not isinstance(name, str) or not name:
                raise ResolutionError("track name must be a non-empty string")
            raw_clips = require_field(track_obj, "clips", f"track {name!r}")
            if not isinstance(raw_clips, list):
                raise ResolutionError(f"track {name!r}: 'clips' must be an array")
            tracks.append(
                Track.from_clips(
                    name,
                    (_parse_clip(raw, fps, name) for raw in raw_clips),
                )
            )
        return tracks
    raw_clips = require_field(data, "clips", "document")
    if not isinstance(raw_clips, list):
        raise ResolutionError("field 'clips' must be an array")
    return [
        Track.from_clips(
            "default",
            (_parse_clip(raw, fps, "default") for raw in raw_clips),
        )
    ]


def loads(data: Any) -> Timeline:
    """Parse a v1 JSON document (already decoded) into a :class:`Timeline`."""
    document = require_object(data)
    check_version(document, VERSION)
    fps = require_int(require_field(document, "fps", "document"), "fps")
    if fps <= 0:
        raise ResolutionError(f"fps must be positive, got {fps}")
    timeline = Timeline(fps, tuple(_parse_tracks(document, fps)))
    timeline.validate_unique_ids()
    return timeline


def load(path: str | Path) -> Timeline:
    return loads(load_json(path))


def dumps(timeline: Timeline) -> dict[str, Any]:
    """Serialize a :class:`Timeline` to a v1 JSON-compatible dict."""
    return {
        "version": VERSION,
        "fps": timeline.fps,
        "tracks": [
            {
                "name": track.name,
                "clips": [
                    {
                        "id": clip.clip_id,
                        "start_seconds": format_fraction(
                            Fraction(clip.interval.start, timeline.fps)
                        ),
                        "end_seconds": format_fraction(
                            Fraction(clip.interval.end, timeline.fps)
                        ),
                        "priority": clip.priority,
                    }
                    for clip in track.clips
                ],
            }
            for track in timeline.tracks
        ],
    }


def dump(timeline: Timeline, path: str | Path) -> None:
    dump_json(dumps(timeline), path)
