"""Exchange format v1: integer project rate, start/duration frame counts.

v1 has no explicit track mode; tracks are read as exclusive.  Layered
tracks and non-integer rates are not representable and are rejected on
write unless ``quantize`` is requested, in which case every frame count
is rounded to the nearest integer and the maximum error is reported.
"""

from __future__ import annotations

from fractions import Fraction
from pathlib import Path
from typing import Any, Union

from ..model import Clip, Gap, MediaRef, Timeline, Track
from .common import FormatError, read_document, require, write_document

VERSION = 1


def loads(doc: dict[str, Any]) -> Timeline:
    if doc.get("version") != VERSION:
        raise FormatError(f"v1 reader: expected version 1, got {doc.get('version')!r}")
    rate = require(doc, "rate", "v1")
    if isinstance(rate, bool) or not isinstance(rate, int) or rate <= 0:
        raise FormatError(f"v1: 'rate' must be a positive integer, got {rate!r}")
    tracks: list[Track] = []
    for ti, tdoc in enumerate(require(doc, "tracks", "v1")):
        ctx = f"v1 track[{ti}]"
        items: list[Any] = []
        for ii, idoc in enumerate(require(tdoc, "items", ctx)):
            ictx = f"{ctx} item[{ii}]"
            kind = require(idoc, "type", ictx)
            if kind == "gap":
                items.append(Gap(start=_frames(idoc, "start", ictx),
                                 duration=_frames(idoc, "duration", ictx)))
            elif kind == "clip":
                mdoc = require(idoc, "media", ictx)
                media = MediaRef(
                    name=str(require(mdoc, "name", ictx + " media")),
                    uri=mdoc.get("uri"),
                    rate=Fraction(require(mdoc, "rate", ictx + " media")),
                    duration=(Fraction(mdoc["duration"])
                              if mdoc.get("duration") is not None else None),
                )
                items.append(Clip(
                    clip_id=str(require(idoc, "id", ictx)),
                    name=str(idoc.get("name", idoc["id"])),
                    start=_frames(idoc, "start", ictx),
                    duration=_frames(idoc, "duration", ictx),
                    source_in=_frames(idoc, "source_in", ictx),
                    source_out=_frames(idoc, "source_out", ictx),
                    media=media,
                    priority=int(idoc.get("priority", 0)),
                ))
            else:
                raise FormatError(f"{ictx}: unknown item type {kind!r}")
        tracks.append(Track(
            name=str(require(tdoc, "name", ctx)),
            kind=str(tdoc.get("kind", "video")),
            mode="exclusive",
            items=items,
        ))
    return Timeline(name=str(doc.get("name", "timeline")), rate=Fraction(rate),
                    tracks=tracks)


def _frames(doc: dict[str, Any], key: str, ctx: str) -> Fraction:
    value = require(doc, key, ctx)
    if isinstance(value, bool) or not isinstance(value, int):
        raise FormatError(f"{ctx}: {key!r} must be an integer frame count, got {value!r}")
    return Fraction(value)


def dumps(timeline: Timeline, quantize: bool = False) -> tuple[dict[str, Any], Fraction]:
    """Serialize to a v1 document.  Returns (doc, max_quantization_error_frames)."""
    rate = timeline.rate
    max_error = Fraction(0)
    if rate.denominator != 1:
        if not quantize:
            raise FormatError(
                f"v1 export: non-integer rate {rate} is not representable; "
                f"pass quantize=True to round to integer frames"
            )
        rate = Fraction(round(rate.numerator / rate.denominator))
        if rate <= 0:
            raise FormatError(f"v1 export: rate {timeline.rate} rounds to zero")

    def q(value: Fraction, ctx: str) -> int:
        nonlocal max_error
        if value.denominator == 1:
            return value.numerator
        if not quantize:
            raise FormatError(
                f"v1 export: {ctx} = {value} is not an integer frame count; "
                f"pass quantize=True to round"
            )
        rounded = round(value.numerator / value.denominator)
        max_error = max(max_error, abs(value - rounded))
        return int(rounded)

    tracks = []
    for track in timeline.tracks:
        if track.mode != "exclusive":
            raise FormatError(
                f"v1 export: track {track.name!r} mode {track.mode!r} is not "
                f"representable in v1 (tracks are implicitly exclusive)"
            )
        items = []
        for item in track.items:
            if isinstance(item, Gap):
                items.append({"type": "gap",
                              "start": q(item.start, f"gap start on {track.name}"),
                              "duration": q(item.duration, f"gap duration on {track.name}")})
            else:
                ctx = f"clip {item.clip_id}"
                items.append({
                    "type": "clip",
                    "id": item.clip_id,
                    "name": item.name,
                    "start": q(item.start, f"{ctx} start"),
                    "duration": q(item.duration, f"{ctx} duration"),
                    "source_in": q(item.source_in, f"{ctx} source_in"),
                    "source_out": q(item.source_out, f"{ctx} source_out"),
                    "priority": item.priority,
                    "media": {
                        "name": item.media.name,
                        "uri": item.media.uri,
                        "rate": q(item.media.rate, f"{ctx} media rate"),
                        **({"duration": q(item.media.duration, f"{ctx} media duration")}
                           if item.media.duration is not None else {}),
                    },
                })
        tracks.append({"name": track.name, "kind": track.kind, "items": items})
    doc = {"version": VERSION, "name": timeline.name, "rate": int(rate),
           "tracks": tracks}
    return doc, max_error


def load(path: Union[str, Path]) -> Timeline:
    return loads(read_document(path))


def dump(timeline: Timeline, path: Union[str, Path], quantize: bool = False) -> Fraction:
    doc, max_error = dumps(timeline, quantize=quantize)
    write_document(path, doc)
    return max_error
