"""Normalize sniff results into serializable media and track summaries.

``extract`` is the single place durations are converted to seconds, so the
manifest written by ``pack`` and the checks run by ``compare``/``scan`` can
never disagree about metadata for the same file.
"""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from . import MANIFEST_VERSION
from .sniff import SniffError, SniffResult, SniffTrack, sniff_path

MEDIA_KINDS = ("video", "audio", "subtitle", "timecode", "hint", "other")
_REASON_NULL = "value unavailable from container header"


class ExtractError(Exception):
    """A file could not be read or understood; carries a locatable payload."""

    def __init__(self, path: str, code: str, message: str, *,
                 offset: int | None = None, box_path: str | None = None):
        super().__init__(message)
        self.path = path
        self.code = code
        self.message = message
        self.offset = offset
        self.box_path = box_path

    def to_dict(self) -> dict:
        return {
            "path": self.path,
            "code": self.code,
            "message": self.message,
            "offset": self.offset,
            "box_path": self.box_path,
        }


def _seconds(units: int | None, timescale: int | None) -> float | None:
    if units is None or timescale is None or timescale <= 0:
        return None
    return units / timescale


def normalize_kind(handler: str | None) -> str:
    if handler == "video":
        return "video"
    if handler == "audio":
        return "audio"
    if handler in ("text", "subtitle"):
        return "subtitle"
    if handler == "timecode":
        return "timecode"
    if handler == "hint":
        return "hint"
    return "other"


def _track_summary(track: SniffTrack, index: int, *,
                   movie_timescale: int | None = None,
                   movie_duration_units: int | None = None
                   ) -> dict[str, Any]:
    reasons = dict(track.reasons)
    duration_s = _seconds(track.duration_units, track.timescale)
    start_units = track.start_time_units
    if start_units is None and track.timescale:
        # Absent edit list means the media starts at zero.
        start_units = 0
    start_s = _seconds(start_units, track.timescale)
    # An empty leading edit delays presentation. Convert it from movie units
    # to seconds so every duration in the summary lives on one time base.
    presentation_s = _seconds(track.start_movie_units, movie_timescale)
    if presentation_s is None:
        presentation_s = 0.0
    if start_s is not None:
        start_s += presentation_s
    if duration_s is None:
        reasons.setdefault("duration_seconds", _REASON_NULL)
    if start_s is None:
        reasons.setdefault("start_seconds", _REASON_NULL)

    entry: dict[str, Any] = {
        "index": index,
        "track_id": track.track_id,
        "kind": normalize_kind(track.handler),
        "handler": track.handler,
        "codec": track.codec,
        "codec_raw": track.codec_raw,
        "timescale": track.timescale,
        "duration_units": track.duration_units,
        "duration_seconds": _round(duration_s),
        "start_units": start_units,
        "presentation_offset_seconds": _round(presentation_s),
        "start_seconds": _round(start_s),
        "language": track.language,
        "width": track.width,
        "height": track.height,
        "sample_rate": track.sample_rate,
        "channels": track.channels,
        "bit_depth": track.bit_depth,
        "reasons": reasons,
        "warnings": [w.to_dict() for w in track.warnings],
    }
    for key in ("track_id", "kind", "codec", "duration_seconds", "start_seconds"):
        if entry[key] is None:
            reasons.setdefault(key, _REASON_NULL)
    return entry


def _round(value: float | None, digits: int = 6) -> float | None:
    if value is None:
        return None
    return round(value, digits)
