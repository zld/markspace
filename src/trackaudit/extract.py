"""Track and sample-table metadata extraction from MP4 containers.

Walks moov/trak/mdia/minf/stbl and pulls track identity, timing and codec
parameters out of tkhd, mdhd, hdlr, stsd and stts. Optional boxes that are
missing leave their fields as None; ``sources`` records which box each
populated field came from and ``notes`` explain the gaps.
"""
from __future__ import annotations

import hashlib
import os
import struct
from dataclasses import dataclass, field

from . import boxes

EXTRACTOR_VERSION = 1

HANDLER_NAMES = {
    "vide": "video", "soun": "audio", "text": "subtitle",
    "sbtl": "subtitle", "subt": "subtitle", "hint": "hint",
    "meta": "metadata",
}

TRACK_FIELDS = ("track_id", "media_type", "language", "timescale",
                "duration", "duration_ms", "movie_duration_ms",
                "sample_duration_ms", "codec", "width", "height",
                "sample_rate", "channels")


@dataclass
class TrackInfo:
    track_id: int | None = None
    media_type: str | None = None
    language: str | None = None
    timescale: int | None = None
    duration: int | None = None            # mdhd, media timescale units
    duration_ms: float | None = None       # mdhd declared, in milliseconds
    movie_duration_ms: float | None = None  # tkhd, in milliseconds
    sample_duration_ms: float | None = None  # derived from stts entries
    codec: str | None = None
    width: int | None = None
    height: int | None = None
    sample_rate: int | None = None
    channels: int | None = None
    sources: dict = field(default_factory=dict)
    notes: list = field(default_factory=list)

    def to_dict(self) -> dict:
        return {k: getattr(self, k) for k in TRACK_FIELDS} | {
            "sources": dict(sorted(self.sources.items())),
            "notes": list(self.notes),
        }

    @classmethod
    def from_dict(cls, data: dict) -> "TrackInfo":
        track = cls(**{k: data.get(k) for k in TRACK_FIELDS})
        track.sources = dict(data.get("sources") or {})
        track.notes = list(data.get("notes") or [])
        return track


@dataclass
class FileInfo:
    path: str
    sha256: str | None = None
    size: int = 0
    ok: bool = True
    tracks: list = field(default_factory=list)
    movie_timescale: int | None = None
    duration_ms: float | None = None  # longest declared track duration
    codec_summary: str = ""
    unknown_boxes: dict = field(default_factory=dict)
    errors: list = field(default_factory=list)
    cached: bool = False

    def to_dict(self) -> dict:
        return {
            "path": self.path, "sha256": self.sha256, "size": self.size,
            "ok": self.ok,
            "tracks": [t.to_dict() for t in self.tracks],
            "movie_timescale": self.movie_timescale,
            "duration_ms": self.duration_ms,
            "codec_summary": self.codec_summary,
            "unknown_boxes": dict(sorted(self.unknown_boxes.items())),
            "errors": list(self.errors),
        }

    @classmethod
    def from_dict(cls, data: dict) -> "FileInfo":
        info = cls(path=data["path"], sha256=data.get("sha256"),
                   size=data.get("size", 0), ok=data.get("ok", True),
                   movie_timescale=data.get("movie_timescale"),
                   duration_ms=data.get("duration_ms"),
                   codec_summary=data.get("codec_summary", ""),
                   unknown_boxes=dict(data.get("unknown_boxes") or {}),
                   errors=list(data.get("errors") or []))
        info.tracks = [TrackInfo.from_dict(t) for t in data.get("tracks", [])]
        return info


def sha256_file(path, chunk_size: int = 1 << 20) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(chunk_size), b""):
            digest.update(chunk)
    return digest.hexdigest()


def extract(path, digest: str | None = None) -> FileInfo:
    """Extract track metadata from one MP4 file.

    Structural problems are captured in ``info.errors`` (info.ok == False)
    rather than raised, so batch processing can continue with other files.
    """
    info = FileInfo(path=str(path))
    try:
        info.size = os.path.getsize(path)
        info.sha256 = digest if digest is not None else sha256_file(path)
        tree = boxes.read_box_tree(path)
    except boxes.BoxError as exc:
        info.ok = False
        info.errors.append(exc.to_dict())
        return info
    except OSError as exc:
        info.ok = False
        info.errors.append({"category": boxes.ERR_IO, "offset": 0,
                            "message": str(exc)})
        return info
    info.unknown_boxes = boxes.unknown_summary(tree)
    try:
        with open(path, "rb") as fh:
            _parse_moov(fh, tree, info)
    except boxes.BoxError as exc:
        info.ok = False
        info.errors.append(exc.to_dict())
        info.tracks = []
        return info
    _finalize(info)
    return info


def _need(payload: bytes, count: int, box: boxes.Box) -> None:
    if len(payload) < count:
        raise boxes.BoxError(boxes.ERR_TRUNCATED, box.offset,
                             f"box '{box.type}' payload is {len(payload)} "
                             f"bytes, need at least {count}")


def _parse_moov(fh, tree: list[boxes.Box], info: FileInfo) -> None:
    moov = next((b for b in tree if b.type == "moov"), None)
    if moov is None:
        raise boxes.BoxError(boxes.ERR_NOT_MP4, 0, "missing moov box")
    mvhd = moov.first("mvhd")
    movie_timescale = None
    if mvhd is not None:
        payload = boxes.read_payload(fh, mvhd)
        _need(payload, 20, mvhd)
        if payload[0] == 1:
            _need(payload, 32, mvhd)
            movie_timescale = struct.unpack_from(">I", payload, 20)[0]
        else:
            movie_timescale = struct.unpack_from(">I", payload, 12)[0]
        info.movie_timescale = movie_timescale
    for trak in moov.find("trak"):
        info.tracks.append(_parse_trak(fh, trak, movie_timescale))


def _parse_trak(fh, trak: boxes.Box, movie_timescale) -> TrackInfo:
    track = TrackInfo()
    tkhd = trak.first("tkhd")
    if tkhd is not None:
        payload = boxes.read_payload(fh, tkhd)
        _need(payload, 84, tkhd)
        if payload[0] == 1:
            _need(payload, 96, tkhd)
            track.track_id = struct.unpack_from(">I", payload, 20)[0]
            tk_duration = struct.unpack_from(">Q", payload, 28)[0]
            w_off = 88
        else:
            track.track_id = struct.unpack_from(">I", payload, 12)[0]
            tk_duration = struct.unpack_from(">I", payload, 20)[0]
            w_off = 76
        track.sources["track_id"] = "trak.tkhd"
        if movie_timescale:
            track.movie_duration_ms = round(
                tk_duration / movie_timescale * 1000, 3)
            track.sources["movie_duration_ms"] = "trak.tkhd"
        width = struct.unpack_from(">I", payload, w_off)[0] >> 16
        height = struct.unpack_from(">I", payload, w_off + 4)[0] >> 16
        if width:
            track.width = width
            track.sources["width"] = "trak.tkhd"
        if height:
            track.height = height
            track.sources["height"] = "trak.tkhd"
    else:
        track.notes.append("tkhd missing: track_id and movie duration "
                           "unavailable")
    mdia = trak.first("mdia")
    if mdia is None:
        track.notes.append("mdia missing: media metadata unavailable")
        return track
    mdhd = mdia.first("mdhd")
    if mdhd is not None:
        _parse_mdhd(boxes.read_payload(fh, mdhd), mdhd, track)
    else:
        track.notes.append("mdhd missing: timescale, duration and language "
                           "unavailable")
    hdlr = mdia.first("hdlr")
    if hdlr is not None:
        payload = boxes.read_payload(fh, hdlr)
        _need(payload, 12, hdlr)
        handler = payload[8:12].decode("latin-1")
        track.media_type = HANDLER_NAMES.get(handler, "other")
        track.sources["media_type"] = "mdia.hdlr"
    else:
        track.notes.append("hdlr missing: media type unavailable")
    minf = mdia.first("minf")
    stbl = minf.first("stbl") if minf is not None else None
    if stbl is None:
        track.notes.append("stbl missing: codec and sample table "
                           "unavailable")
        return track
    stsd = stbl.first("stsd")
    if stsd is not None:
        _parse_stsd(boxes.read_payload(fh, stsd), stsd, track)
    else:
        track.notes.append("stsd missing: codec parameters unavailable")
    stts = stbl.first("stts")
    if stts is not None:
        total = _parse_stts(boxes.read_payload(fh, stts), stts)
        if track.timescale:
            track.sample_duration_ms = round(
                total / track.timescale * 1000, 3)
            track.sources["sample_duration_ms"] = "mdia.minf.stbl.stts"
    else:
        track.notes.append("stts missing: sample-table duration unavailable")
    return track


def _parse_mdhd(payload: bytes, box: boxes.Box, track: TrackInfo) -> None:
    _need(payload, 24, box)
    if payload[0] == 1:
        _need(payload, 32, box)
        timescale = struct.unpack_from(">I", payload, 20)[0]
        duration = struct.unpack_from(">Q", payload, 24)[0]
        lang_off = 32
    else:
        timescale = struct.unpack_from(">I", payload, 12)[0]
        duration = struct.unpack_from(">I", payload, 16)[0]
        lang_off = 20
    track.timescale = timescale
    track.duration = duration
    track.sources["timescale"] = "mdia.mdhd"
    track.sources["duration"] = "mdia.mdhd"
    if timescale:
        track.duration_ms = round(duration / timescale * 1000, 3)
        track.sources["duration_ms"] = "mdia.mdhd"
    language = _decode_language(struct.unpack_from(">H", payload,
                                                   lang_off)[0])
    if language:
        track.language = language
        track.sources["language"] = "mdia.mdhd"


def _decode_language(bits: int) -> str | None:
    chars = [((bits >> 10) & 0x1F) + 0x60, ((bits >> 5) & 0x1F) + 0x60,
             (bits & 0x1F) + 0x60]
    if not all(0x61 <= c <= 0x7A for c in chars):
        return None
    return "".join(map(chr, chars))


def _parse_stsd(payload: bytes, box: boxes.Box, track: TrackInfo) -> None:
    _need(payload, 8, box)
    entry_count = struct.unpack_from(">I", payload, 4)[0]
    offset = 8
    for _ in range(min(entry_count, 16)):
        if offset + 8 > len(payload):
            break
        entry_size, = struct.unpack_from(">I", payload, offset)
        if entry_size < 8 or offset + entry_size > len(payload):
            break
        fourcc = payload[offset + 4:offset + 8].decode("latin-1")
        if track.codec is None:
            track.codec = fourcc
            track.sources["codec"] = "mdia.minf.stbl.stsd"
            body = offset + 8
            if track.media_type == "video" and body + 28 <= len(payload):
                track.width = struct.unpack_from(">H", payload, body + 24)[0]
                track.height = struct.unpack_from(">H", payload,
                                                  body + 26)[0]
                track.sources["width"] = "mdia.minf.stbl.stsd"
                track.sources["height"] = "mdia.minf.stbl.stsd"
            elif track.media_type == "audio" and body + 28 <= len(payload):
                track.channels = struct.unpack_from(">H", payload,
                                                    body + 16)[0]
                rate = struct.unpack_from(">I", payload, body + 24)[0]
                track.sample_rate = rate >> 16
                track.sources["channels"] = "mdia.minf.stbl.stsd"
                track.sources["sample_rate"] = "mdia.minf.stbl.stsd"
        offset += entry_size


def _parse_stts(payload: bytes, box: boxes.Box) -> int:
    _need(payload, 8, box)
    entry_count = struct.unpack_from(">I", payload, 4)[0]
    if entry_count * 8 > len(payload) - 8:
        raise boxes.BoxError(boxes.ERR_OUT_OF_BOUNDS, box.offset,
                             f"stts declares {entry_count} entries but only "
                             f"{(len(payload) - 8) // 8} fit")
    total = 0
    for i in range(entry_count):
        count, delta = struct.unpack_from(">II", payload, 8 + i * 8)
        total += count * delta
    return total


def _codec_label(track: TrackInfo) -> str:
    codec = track.codec or "?"
    if track.media_type == "video" and track.width and track.height:
        return f"{codec} {track.width}x{track.height}"
    if track.media_type == "audio" and track.sample_rate:
        ch = f" {track.channels}ch" if track.channels else ""
        return f"{codec} {track.sample_rate}Hz{ch}"
    return codec


def _finalize(info: FileInfo) -> None:
    durations = [t.duration_ms for t in info.tracks
                 if t.duration_ms is not None]
    info.duration_ms = max(durations) if durations else None
    ordered = sorted(info.tracks,
                     key=lambda t: (t.track_id is None, t.track_id or 0))
    info.codec_summary = ",".join(_codec_label(t) for t in ordered)
