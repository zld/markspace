"""Minimal MP4 reader used by `inspect` and by output validation."""
from __future__ import annotations

import struct


class Mp4Error(Exception):
    """MP4 structure is corrupt or incomplete."""


def _read_boxes(data: bytes, start: int, end: int):
    pos = start
    while pos + 8 <= end:
        size, tag = struct.unpack_from(">I4s", data, pos)
        header = 8
        if size == 1:
            if pos + 16 > end:
                raise Mp4Error(f"truncated largesize box at offset {pos}")
            size, = struct.unpack_from(">Q", data, pos + 8)
            header = 16
        elif size == 0:
            size = end - pos
        if size < header or pos + size > end:
            raise Mp4Error(f"invalid box {tag!r} at offset {pos}")
        yield tag, pos + header, pos + size
        pos += size
    if pos != end:
        raise Mp4Error(f"trailing {end - pos} bytes at offset {pos}")


def _children(data: bytes, start: int, end: int, want: bytes):
    return [(s, e) for tag, s, e in _read_boxes(data, start, end) if tag == want]


def _child(data: bytes, start: int, end: int, want: bytes):
    found = _children(data, start, end, want)
    return found[0] if found else None


def _parse_mdhd(data: bytes, s: int, e: int):
    version = data[s]
    if version == 1:
        timescale, duration = struct.unpack_from(">IQ", data, s + 20)
    else:
        timescale, duration = struct.unpack_from(">II", data, s + 12)
    if timescale <= 0:
        raise Mp4Error("mdhd has zero timescale")
    return timescale, duration


def _parse_stts(data: bytes, s: int):
    (count,) = struct.unpack_from(">I", data, s + 4)
    entries, pos = [], s + 8
    for _ in range(count):
        entries.append(struct.unpack_from(">II", data, pos))
        pos += 8
    return entries


def _parse_trak(data: bytes, s: int, e: int) -> dict:
    mdia = _child(data, s, e, b"mdia")
    if not mdia:
        raise Mp4Error("trak without mdia")
    mdhd = _child(data, *mdia, b"mdhd")
    hdlr = _child(data, *mdia, b"hdlr")
    if not mdhd or not hdlr:
        raise Mp4Error("trak missing mdhd or hdlr")
    timescale, _ = _parse_mdhd(data, *mdhd)
    kind = data[hdlr[0] + 8:hdlr[0] + 12].decode("latin1")
    minf = _child(data, *mdia, b"minf")
    stbl = _child(data, *minf, b"stbl") if minf else None
    if not stbl:
        raise Mp4Error("trak missing stbl")
    stts = _child(data, *stbl, b"stts")
    stsz = _child(data, *stbl, b"stsz")
    if not stts or not stsz:
        raise Mp4Error("trak missing stts or stsz")
    entries = _parse_stts(data, *stts)
    sample_count = sum(c for c, _ in entries)
    declared, = struct.unpack_from(">I", data, stsz[0] + 8)
    if declared != sample_count:
        raise Mp4Error("stsz/stts sample count mismatch")
    total = sum(c * d for c, d in entries)
    last_delta = entries[-1][1] if entries else 0
    stss = _child(data, *stbl, b"stss")
    keyframes = struct.unpack_from(">I", data, stss[0] + 4)[0] if stss else 0
    return {
        "type": "video" if kind == "vide" else "audio" if kind == "soun" else kind,
        "timescale": timescale,
        "sample_count": sample_count,
        "keyframe_count": keyframes,
        "first_pts_ms": 0.0,
        "last_pts_ms": (total - last_delta) * 1000.0 / timescale,
        "duration_ms": total * 1000.0 / timescale,
    }


def inspect_mp4(path: str) -> dict:
    with open(path, "rb") as fh:
        data = fh.read()
    tags = [tag for tag, _, _ in _read_boxes(data, 0, len(data))]
    if not tags or tags[0] != b"ftyp":
        raise Mp4Error("not an MP4 file (missing ftyp)")
    tracks = []
    for ms, me in _children(data, 0, len(data), b"moov"):
        for ts, te in _children(data, ms, me, b"trak"):
            tracks.append(_parse_trak(data, ts, te))
    if not tracks:
        raise Mp4Error("MP4 file has no tracks")
    return {
        "format": "mp4",
        "file_size": len(data),
        "track_count": len(tracks),
        "sample_count": sum(t["sample_count"] for t in tracks),
        "tracks": tracks,
    }
