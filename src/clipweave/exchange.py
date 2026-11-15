"""Custom exchange format, versions 1 and 2.

v1: timeline frame rate plus integer frame intervals; no source offsets
    or metadata.
v2: exact rational seconds, per-clip source offsets and metadata.

Both are JSON documents with ``format: "clipweave"`` and a ``version``
field. ``loads`` dispatches on the version; ``dumps`` picks a writer.
Downgrading to v1 raises :class:`DowngradeError` when data would be
lost. ``roundtrip`` serializes and re-parses so callers can verify the
model survives unchanged.
"""
from __future__ import annotations

import json
from fractions import Fraction
from typing import Any

from .errors import DowngradeError, FormatError, ValidationError
from .model import (
    DEFAULT_MAX_CLIPS,
    Clip,
    Timeline,
    Track,
    as_time,
    build_timeline,
    frames,
    validate_timeline,
)

FORMAT_NAME = "clipweave"
SUPPORTED_VERSIONS = (1, 2)


# ---------------------------------------------------------------- writers

def _clip_to_v1(clip: Clip, fps: Fraction) -> dict:
    if clip.source_start != 0:
        raise DowngradeError(
            "v1 cannot carry a source offset", clip_id=clip.id
        )
    if clip.metadata:
        raise DowngradeError("v1 cannot carry metadata", clip_id=clip.id)
    try:
        start_frame = frames(clip.start, fps)
        duration_frames = frames(clip.duration, fps)
    except ValidationError as exc:
        raise DowngradeError(str(exc), clip_id=clip.id) from exc
    return {
        "id": clip.id,
        "start_frame": start_frame,
        "duration_frames": duration_frames,
        "priority": clip.priority,
    }


def _clip_to_v2(clip: Clip) -> dict:
    return {
        "id": clip.id,
        "start": str(clip.start),
        "duration": str(clip.duration),
        "source_start": str(clip.source_start),
        "priority": clip.priority,
        "metadata": dict(clip.metadata),
    }


def dumps(timeline: Timeline, version: int) -> str:
    if version == 1:
        clip_fn = lambda c: _clip_to_v1(c, timeline.fps)  # noqa: E731
    elif version == 2:
        clip_fn = _clip_to_v2
    else:
        raise FormatError(f"unknown exchange version: {version!r}")
    doc = {
        "format": FORMAT_NAME,
        "version": version,
        "name": timeline.name,
        "fps": str(timeline.fps),
        "tracks": [
            {"name": track.name, "clips": [clip_fn(c) for c in track.clips]}
            for track in timeline.tracks
        ],
    }
    try:
        return json.dumps(doc, indent=2, sort_keys=False) + "\n"
    except TypeError as exc:
        raise FormatError(f"document is not JSON-serializable: {exc}") from exc


# ---------------------------------------------------------------- readers

def _fail(message: str) -> FormatError:
    return FormatError(message)


def _expect_object(value: Any, what: str) -> dict:
    if not isinstance(value, dict):
        raise _fail(f"{what} must be a JSON object")
    return value


def _parse_time(raw: Any, what: str) -> Fraction:
    if isinstance(raw, float):
        raise _fail(f"{what} must be an exact rational, not a float: {raw!r}")
    try:
        return as_time(raw)
    except ValidationError as exc:
        raise _fail(f"{what}: {exc.message}")


def _parse_common(doc: dict) -> tuple[str, Fraction, list]:
    if doc.get("format") != FORMAT_NAME:
        raise _fail(f"not a {FORMAT_NAME} document")
    name = doc.get("name", "timeline")
    if not isinstance(name, str):
        raise _fail("name must be a string")
    if "fps" not in doc:
        raise _fail("missing fps")
    fps = _parse_time(doc["fps"], "fps")
    raw_tracks = doc.get("tracks")
    if not isinstance(raw_tracks, list):
        raise _fail("tracks must be a list")
    return name, fps, raw_tracks


def _parse_clip_id(raw: Any) -> str:
    if not isinstance(raw, str) or not raw:
        raise _fail("clip id must be a non-empty string")
    return raw


def _parse_priority(raw: Any) -> int:
    if raw is None:
        return 0
    if isinstance(raw, bool) or not isinstance(raw, int):
        raise _fail(f"priority must be an integer, got {raw!r}")
    return raw


def _read_v1(doc: dict) -> Timeline:
    name, fps, raw_tracks = _parse_common(doc)
    tracks = []
    for raw_track in raw_tracks:
        track_doc = _expect_object(raw_track, "track")
        clips = []
        for raw_clip in track_doc.get("clips", []):
            clip_doc = _expect_object(raw_clip, "clip")
            start_frame = clip_doc.get("start_frame")
            duration_frames = clip_doc.get("duration_frames")
            for label, value in (
                ("start_frame", start_frame),
                ("duration_frames", duration_frames),
            ):
                if isinstance(value, bool) or not isinstance(value, int):
                    raise _fail(f"{label} must be an integer, got {value!r}")
            clips.append(
                Clip(
                    id=_parse_clip_id(clip_doc.get("id")),
                    start=Fraction(start_frame) / fps,
                    duration=Fraction(duration_frames) / fps,
                    priority=_parse_priority(clip_doc.get("priority")),
                )
            )
        tracks.append((str(track_doc.get("name", "track")), clips))
    return build_timeline(fps=fps, tracks=tracks, name=name)


def _read_v2(doc: dict) -> Timeline:
    name, fps, raw_tracks = _parse_common(doc)
    tracks = []
    for raw_track in raw_tracks:
        track_doc = _expect_object(raw_track, "track")
        clips = []
        for raw_clip in track_doc.get("clips", []):
            clip_doc = _expect_object(raw_clip, "clip")
            metadata = clip_doc.get("metadata", {})
            if not isinstance(metadata, dict):
                raise _fail("metadata must be a JSON object")
            clips.append(
                Clip(
                    id=_parse_clip_id(clip_doc.get("id")),
                    start=_parse_time(clip_doc.get("start", "0"), "start"),
                    duration=_parse_time(
                        clip_doc.get("duration", "0"), "duration"
                    ),
                    priority=_parse_priority(clip_doc.get("priority")),
                    source_start=_parse_time(
                        clip_doc.get("source_start", "0"), "source_start"
                    ),
                    metadata=metadata,
                )
            )
        tracks.append((str(track_doc.get("name", "track")), clips))
    return build_timeline(fps=fps, tracks=tracks, name=name)


def loads(text: str, *, max_clips: int = DEFAULT_MAX_CLIPS) -> Timeline:
    """Parse an exchange document and validate the resulting model."""
    try:
        doc = json.loads(text)
    except json.JSONDecodeError as exc:
        raise _fail(f"invalid JSON: {exc}")
    doc = _expect_object(doc, "document")
    version = doc.get("version")
    if version == 1:
        timeline = _read_v1(doc)
    elif version == 2:
        timeline = _read_v2(doc)
    else:
        raise _fail(f"unknown exchange version: {version!r}")
    return validate_timeline(timeline, max_clips=max_clips)


def roundtrip(timeline: Timeline, version: int) -> Timeline:
    """Serialize then re-parse; the result should equal the input."""
    return loads(dumps(timeline, version))


# ---------------------------------------------------------------- file IO

def load_file(path: str, *, max_clips: int = DEFAULT_MAX_CLIPS) -> Timeline:
    try:
        with open(path, "r", encoding="utf-8") as fh:
            text = fh.read()
    except OSError as exc:
        raise _fail(f"cannot read {path}: {exc}")
    return loads(text, max_clips=max_clips)


def dump_file(timeline: Timeline, path: str, version: int) -> None:
    text = dumps(timeline, version)
    try:
        with open(path, "w", encoding="utf-8") as fh:
            fh.write(text)
    except OSError as exc:
        raise _fail(f"cannot write {path}: {exc}")
