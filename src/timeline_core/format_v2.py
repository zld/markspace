"""Second-version JSON exchange format.

Layout::

    {
      "version": 2,
      "rate": {"numerator": 24, "denominator": 1},
      "tracks": [
        {"name": "V1",
         "clips": [
           {"clip_id": "A", "start_frame": 0, "end_frame": 120,
            "priority": 1}
         ]}
      ]
    }

Differences from v1:

* the frame rate is an explicit rational ``rate`` object (denominator
  defaults to 1); its denominator must be 1, because the canonical model is
  an *integer* frame scale;
* clips carry stable IDs in ``clip_id`` and integer frame positions
  (``start_frame`` / ``end_frame``) instead of seconds, so no rounding occurs
  inside this reader.

Reading never depends on record order: frames are deterministic from the
data alone, which the test suite proves with reversed documents.
"""

from __future__ import annotations

from pathlib import Path
from typing import Any

from .format_common import (
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
)

VERSION = 2


def _parse_rate(data: dict[str, Any]) -> int:
    raw_rate = require_field(data, "rate", "document")
    rate = require_object(raw_rate)
    numerator = require_int(
        require_field(rate, "numerator", "rate"), "rate.numerator"
    )
    denominator = require_int(
        rate.get("denominator", 1), "rate.denominator"
    )
    if denominator <= 0:
        raise ResolutionError(
            f"rate.denominator must be positive, got {denominator}"
        )
    if numerator <= 0:
        raise ResolutionError(f"rate.numerator must be positive, got {numerator}")
    if numerator % denominator != 0:
        raise ResolutionError(
            "integer frame scale requires an integer frame rate, got "
            f"{numerator}/{denominator}"
        )
    return numerator // denominator


def _parse_clip(raw: Any, track_name: str) -> Clip:
    obj = require_object(raw)
    context = f"clip in track {track_name!r}"
    clip_id = require_field(obj, "clip_id", context)
    if not isinstance(clip_id, str) or not clip_id:
        raise ResolutionError(
            f"clip_id must be a non-empty string, got {clip_id!r}"
        )
    start = require_int(
        require_field(obj, "start_frame", context), "start_frame"
    )
    end = require_int(require_field(obj, "end_frame", context), "end_frame")
    if start < 0:
        raise ResolutionError(
            f"clip {clip_id!r} has negative start_frame {start}"
        )
    if end < start:
        raise ResolutionError(
            f"clip {clip_id!r} has negative duration: "
            f"start_frame={start} end_frame={end}"
        )
    priority = require_int(require_field(obj, "priority", context), "priority")
    return Clip(
        clip_id=clip_id,
        interval=FrameInterval(start, end),
        priority=priority,
        track=track_name,
    )


def _parse_tracks(data: dict[str, Any]) -> list[Track]:
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
                (_parse_clip(raw, name) for raw in raw_clips),
            )
        )
    return tracks


def loads(data: Any) -> Timeline:
    """Parse a v2 JSON document (already decoded) into a :class:`Timeline`."""
    document = require_object(data)
    check_version(document, VERSION)
    fps = _parse_rate(document)
    timeline = Timeline(fps, tuple(_parse_tracks(document)))
    timeline.validate_unique_ids()
    return timeline


def load(path: str | Path) -> Timeline:
    return loads(load_json(path))


def dumps(timeline: Timeline) -> dict[str, Any]:
    """Serialize a :class:`Timeline` to a v2 JSON-compatible dict."""
    return {
        "version": VERSION,
        "rate": {"numerator": timeline.fps, "denominator": 1},
        "tracks": [
            {
                "name": track.name,
                "clips": [
                    {
                        "clip_id": clip.clip_id,
                        "start_frame": clip.interval.start,
                        "end_frame": clip.interval.end,
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
