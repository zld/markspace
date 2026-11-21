"""Exchange format v2: rational rates, half-open ranges, explicit modes.

All times are exact rationals encoded as int or [num, den].  Clip ranges
are half-open [start, end) and every track declares its mode explicitly.
"""

from __future__ import annotations

from pathlib import Path
from typing import Any, Union

from ..model import Clip, Gap, MediaRef, Timeline, Track
from .common import (
    FormatError,
    fraction_from_json,
    fraction_to_json,
    read_document,
    require,
    write_document,
)

VERSION = 2


def _range_from_json(doc: dict[str, Any], ctx: str) -> tuple[Any, Any]:
    rdoc = require(doc, "range", ctx)
    start = fraction_from_json(require(rdoc, "start", ctx + " range"), ctx + " range.start")
    end = fraction_from_json(require(rdoc, "end", ctx + " range"), ctx + " range.end")
    if end < start:
        raise FormatError(f"{ctx}: range end {end} < start {start}")
    return start, end


def loads(doc: dict[str, Any]) -> Timeline:
    if doc.get("version") != VERSION:
        raise FormatError(f"v2 reader: expected version 2, got {doc.get('version')!r}")
    rate = fraction_from_json(require(doc, "rate", "v2"), "v2 rate")
    if rate <= 0:
        raise FormatError(f"v2: rate must be positive, got {rate}")
    tracks: list[Track] = []
    for ti, tdoc in enumerate(require(doc, "tracks", "v2")):
        ctx = f"v2 track[{ti}]"
        mode = require(tdoc, "mode", ctx)
        items: list[Any] = []
        for ii, idoc in enumerate(require(tdoc, "items", ctx)):
            ictx = f"{ctx} item[{ii}]"
            kind = require(idoc, "type", ictx)
            start, end = _range_from_json(idoc, ictx)
            if kind == "gap":
                items.append(Gap(start=start, duration=end - start))
            elif kind == "clip":
                sdoc = require(idoc, "source", ictx)
                source_in = fraction_from_json(require(sdoc, "in", ictx + " source"),
                                               ictx + " source.in")
                source_out = fraction_from_json(require(sdoc, "out", ictx + " source"),
                                                ictx + " source.out")
                mdoc = require(idoc, "media", ictx)
                media = MediaRef(
                    name=str(require(mdoc, "name", ictx + " media")),
                    uri=mdoc.get("uri"),
                    rate=fraction_from_json(require(mdoc, "rate", ictx + " media"),
                                            ictx + " media.rate"),
                    duration=(fraction_from_json(mdoc["duration"], ictx + " media.duration")
                              if mdoc.get("duration") is not None else None),
                )
                items.append(Clip(
                    clip_id=str(require(idoc, "id", ictx)),
                    name=str(idoc.get("name", idoc["id"])),
                    start=start,
                    duration=end - start,
                    source_in=source_in,
                    source_out=source_out,
                    media=media,
                    priority=int(idoc.get("priority", 0)),
                ))
            else:
                raise FormatError(f"{ictx}: unknown item type {kind!r}")
        tracks.append(Track(
            name=str(require(tdoc, "name", ctx)),
            kind=str(tdoc.get("kind", "video")),
            mode=str(mode),
            items=items,
        ))
    return Timeline(name=str(doc.get("name", "timeline")), rate=rate, tracks=tracks)


def dumps(timeline: Timeline) -> dict[str, Any]:
    tracks = []
    for track in timeline.tracks:
        items = []
        for item in track.items:
            rng = {"start": fraction_to_json(item.start),
                   "end": fraction_to_json(item.end)}
            if isinstance(item, Gap):
                items.append({"type": "gap", "range": rng})
            else:
                items.append({
                    "type": "clip",
                    "id": item.clip_id,
                    "name": item.name,
                    "range": rng,
                    "source": {"in": fraction_to_json(item.source_in),
                               "out": fraction_to_json(item.source_out)},
                    "priority": item.priority,
                    "media": {
                        "name": item.media.name,
                        "uri": item.media.uri,
                        "rate": fraction_to_json(item.media.rate),
                        **({"duration": fraction_to_json(item.media.duration)}
                           if item.media.duration is not None else {}),
                    },
                })
        tracks.append({"name": track.name, "kind": track.kind,
                       "mode": track.mode, "items": items})
    return {"version": VERSION, "name": timeline.name,
            "rate": fraction_to_json(timeline.rate), "tracks": tracks}


def load(path: Union[str, Path]) -> Timeline:
    return loads(read_document(path))


def dump(timeline: Timeline, path: Union[str, Path]) -> None:
    write_document(path, dumps(timeline))
