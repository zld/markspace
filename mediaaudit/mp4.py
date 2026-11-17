"""Minimal MP4 (ISO BMFF) metadata parser.

Parses the box hierarchy and extracts, per track: timescale and duration
(from mdhd), handler type (hdlr), codec summary (stsd) and, for video,
frame rate (stts). Box lengths and boundaries are validated; a zero
timescale is rejected. No media data is decoded.
"""
from __future__ import annotations

import struct

VIDEO_CODECS = {
    "avc1": "h264", "avc3": "h264", "hvc1": "h265", "hev1": "h265",
    "vp09": "vp9", "av01": "av1", "mp4v": "mpeg4",
}
AUDIO_CODECS = {
    "mp4a": "aac", "ac-3": "ac3", "ec-3": "eac3",
    "alac": "alac", "Opus": "opus", "sowt": "pcm",
}
HANDLER_TYPES = {"vide": "video", "soun": "audio"}


class MP4Error(Exception):
    """The file violates the supported MP4 metadata subset."""


def _iter_boxes(data: bytes, start: int, end: int):
    """Yield (type, content_start, box_end) for each box in [start, end)."""
    off = start
    while off < end:
        if off + 8 > end:
            raise MP4Error(f"truncated box header at offset {off}")
        size, typ = struct.unpack_from(">I4s", data, off)
        header = 8
        if size == 1:
            if off + 16 > end:
                raise MP4Error(f"truncated largesize at offset {off}")
            size = struct.unpack_from(">Q", data, off + 8)[0]
            header = 16
        elif size == 0:
            size = end - off
        if size < header or off + size > end:
            raise MP4Error(f"invalid box length {size} at offset {off}")
        yield typ, off + header, off + size
        off += size


def _child(data: bytes, start: int, end: int, typ: bytes):
    for t, s, e in _iter_boxes(data, start, end):
        if t == typ:
            return s, e
    return None


def _parse_mdhd(data: bytes, s: int, e: int):
    if s + 4 > e:
        raise MP4Error("truncated mdhd")
    version = data[s]
    if version == 1:
        if s + 32 > e:
            raise MP4Error("truncated mdhd v1")
        timescale, duration = struct.unpack_from(">IQ", data, s + 20)
    else:
        if s + 24 > e:
            raise MP4Error("truncated mdhd v0")
        timescale, duration = struct.unpack_from(">II", data, s + 12)
    if timescale == 0:
        raise MP4Error("zero track timescale")
    return timescale, duration


def _parse_tkhd(data: bytes, s: int, e: int):
    if s + 20 > e or e - 8 < s:
        raise MP4Error("truncated tkhd")
    version = data[s]
    track_id = struct.unpack_from(">I", data, s + (20 if version == 1 else 12))[0]
    width = struct.unpack_from(">I", data, e - 8)[0] >> 16
    height = struct.unpack_from(">I", data, e - 4)[0] >> 16
    return track_id, width, height


def _parse_stsd(data: bytes, s: int, e: int, track_type: str):
    """Return (codec, codec_raw, sample_rate, channels)."""
    if s + 16 > e:
        raise MP4Error("truncated stsd")
    count = struct.unpack_from(">I", data, s + 4)[0]
    if count < 1:
        raise MP4Error("stsd has no sample entries")
    entry = s + 8
    entry_size = struct.unpack_from(">I", data, entry)[0]
    if entry_size < 8 or entry + entry_size > e:
        raise MP4Error("invalid stsd entry length")
    raw = data[entry + 4:entry + 8].decode("latin-1")
    table = AUDIO_CODECS if track_type == "audio" else VIDEO_CODECS
    codec = table.get(raw, f"unknown:{raw}")
    sample_rate = channels = None
    if track_type == "audio":
        if entry + 28 > e:
            raise MP4Error("truncated audio sample entry")
        channels = struct.unpack_from(">H", data, entry + 16)[0]
        sample_rate = struct.unpack_from(">I", data, entry + 24)[0] >> 16
    return codec, raw, sample_rate, channels


def _parse_stts(data: bytes, s: int, e: int, timescale: int):
    """Frame rate from time-to-sample: total_samples * timescale / total_time."""
    if s + 8 > e:
        raise MP4Error("truncated stts")
    count = struct.unpack_from(">I", data, s + 4)[0]
    if s + 8 + count * 8 > e:
        raise MP4Error("truncated stts entries")
    samples = total = 0
    for i in range(count):
        n, delta = struct.unpack_from(">II", data, s + 8 + i * 8)
        samples += n
        total += n * delta
    if total == 0:
        return None
    return samples * timescale / total


def _parse_trak(data: bytes, s: int, e: int) -> dict:
    tkhd = _child(data, s, e, b"tkhd")
    mdia = _child(data, s, e, b"mdia")
    if tkhd is None or mdia is None:
        raise MP4Error("trak missing tkhd or mdia")
    track_id, width, height = _parse_tkhd(data, *tkhd)
    mdhd = _child(data, mdia[0], mdia[1], b"mdhd")
    hdlr = _child(data, mdia[0], mdia[1], b"hdlr")
    if mdhd is None or hdlr is None:
        raise MP4Error("mdia missing mdhd or hdlr")
    timescale, duration = _parse_mdhd(data, *mdhd)
    if hdlr[0] + 12 > hdlr[1]:
        raise MP4Error("truncated hdlr")
    handler = data[hdlr[0] + 8:hdlr[0] + 12].decode("latin-1")
    track_type = HANDLER_TYPES.get(handler, "other")

    codec, raw, sample_rate, channels = f"unknown:{handler}", handler, None, None
    frame_rate = None
    minf = _child(data, mdia[0], mdia[1], b"minf")
    stbl = _child(data, minf[0], minf[1], b"stbl") if minf else None
    if stbl:
        stsd = _child(data, stbl[0], stbl[1], b"stsd")
        if stsd:
            codec, raw, sample_rate, channels = _parse_stsd(data, *stsd, track_type)
        if track_type == "video":
            stts = _child(data, stbl[0], stbl[1], b"stts")
            if stts:
                frame_rate = _parse_stts(data, *stts, timescale)
    return {
        "track_id": track_id,
        "type": track_type,
        "codec": codec,
        "codec_raw": raw,
        "duration_ns": duration * 1_000_000_000 // timescale,
        "width": width if track_type == "video" else None,
        "height": height if track_type == "video" else None,
        "frame_rate": frame_rate,
        "sample_rate": sample_rate,
        "channels": channels,
    }


def parse_mp4_bytes(data: bytes) -> list:
    """Parse MP4 metadata from *data* and return a list of track dicts."""
    moov = None
    for typ, s, e in _iter_boxes(data, 0, len(data)):
        if typ == b"moov":
            moov = (s, e)
            break
    if moov is None:
        raise MP4Error("missing moov box")
    tracks = [
        _parse_trak(data, s, e)
        for typ, s, e in _iter_boxes(data, *moov)
        if typ == b"trak"
    ]
    if not tracks:
        raise MP4Error("no tracks in moov")
    return tracks


def parse_mp4(path: str) -> list:
    with open(path, "rb") as fh:
        return parse_mp4_bytes(fh.read())
