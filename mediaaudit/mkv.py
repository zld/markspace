"""Minimal Matroska (EBML) metadata parser.

Reads EBML variable-length integers, the Info element (TimecodeScale,
Duration) and the Tracks element (per-track codec and encoding
parameters). Unknown-length Segments are supported, unknown elements are
skipped, truncated elements are rejected. No media data is decoded.
"""
from __future__ import annotations

import struct

ID_SEGMENT = 0x18538067
ID_INFO = 0x1549A966
ID_TIMECODE_SCALE = 0x2AD7B1
ID_DURATION = 0x4489
ID_TRACKS = 0x1654AE6B
ID_TRACK_ENTRY = 0xAE
ID_TRACK_NUMBER = 0xD7
ID_TRACK_TYPE = 0x83
ID_CODEC_ID = 0x86
ID_DEFAULT_DURATION = 0x23E383
ID_VIDEO = 0xE0
ID_PIXEL_WIDTH = 0xB0
ID_PIXEL_HEIGHT = 0xBA
ID_AUDIO = 0xE1
ID_SAMPLING_FREQUENCY = 0xB5
ID_CHANNELS = 0x9F

TRACK_TYPES = {1: "video", 2: "audio"}
CODECS = {
    "V_MPEG4/ISO/AVC": "h264", "V_MPEGH/ISO/HEVC": "h265",
    "V_VP9": "vp9", "V_AV1": "av1",
    "A_AAC": "aac", "A_OPUS": "opus", "A_AC3": "ac3", "A_PCM/INT/LIT": "pcm",
}
DEFAULT_TIMECODE_SCALE = 1_000_000  # ns per tick


class MKVError(Exception):
    """The file violates the supported Matroska metadata subset."""


def _read_vint(data: bytes, off: int, end: int, keep_marker: bool):
    """Read an EBML variable-length integer; return (value, length)."""
    if off >= end:
        raise MKVError("truncated variable-length integer")
    first = data[off]
    if first == 0:
        raise MKVError("invalid variable-length integer")
    mask, length = 0x80, 1
    while length <= 8 and not first & mask:
        mask >>= 1
        length += 1
    if length > 8 or off + length > end:
        raise MKVError("invalid variable-length integer")
    value = first if keep_marker else first & (mask - 1)
    for i in range(1, length):
        value = (value << 8) | data[off + i]
    return value, length


def _read_size(data: bytes, off: int, end: int):
    """Return (size, length); size is None for unknown-length elements."""
    value, length = _read_vint(data, off, end, keep_marker=False)
    if value == (1 << (7 * length)) - 1:
        return None, length
    return value, length


def _iter_elements(data: bytes, start: int, end: int):
    """Yield (element_id, content_start, element_end) in [start, end)."""
    off = start
    while off < end:
        eid, n = _read_vint(data, off, end, keep_marker=True)
        off += n
        size, n = _read_size(data, off, end)
        off += n
        if size is None:
            size = end - off
        if off + size > end:
            raise MKVError(f"truncated element 0x{eid:x} at offset {off}")
        yield eid, off, off + size
        off += size


def _uint(data: bytes, s: int, e: int) -> int:
    if not 1 <= e - s <= 8:
        raise MKVError("invalid unsigned integer length")
    return int.from_bytes(data[s:e], "big")


def _float(data: bytes, s: int, e: int) -> float:
    if e - s == 4:
        return struct.unpack(">f", data[s:e])[0]
    if e - s == 8:
        return struct.unpack(">d", data[s:e])[0]
    raise MKVError("invalid float length")


def _parse_info(data: bytes, s: int, e: int):
    scale = DEFAULT_TIMECODE_SCALE
    duration_ns = 0
    for eid, cs, ce in _iter_elements(data, s, e):
        if eid == ID_TIMECODE_SCALE:
            scale = _uint(data, cs, ce)
        elif eid == ID_DURATION:
            duration_ns = int(round(_float(data, cs, ce) * scale))
    if scale <= 0:
        raise MKVError("non-positive TimecodeScale")
    return duration_ns


def _parse_track_entry(data: bytes, s: int, e: int, duration_ns: int) -> dict:
    number, ttype, raw = 0, 0, ""
    width = height = channels = None
    sample_rate = frame_rate = None
    default_duration = None
    for eid, cs, ce in _iter_elements(data, s, e):
        if eid == ID_TRACK_NUMBER:
            number = _uint(data, cs, ce)
        elif eid == ID_TRACK_TYPE:
            ttype = _uint(data, cs, ce)
        elif eid == ID_CODEC_ID:
            raw = data[cs:ce].decode("ascii", "replace")
        elif eid == ID_DEFAULT_DURATION:
            default_duration = _uint(data, cs, ce)
        elif eid == ID_VIDEO:
            for vid, vs, ve in _iter_elements(data, cs, ce):
                if vid == ID_PIXEL_WIDTH:
                    width = _uint(data, vs, ve)
                elif vid == ID_PIXEL_HEIGHT:
                    height = _uint(data, vs, ve)
        elif eid == ID_AUDIO:
            for aid, as_, ae in _iter_elements(data, cs, ce):
                if aid == ID_SAMPLING_FREQUENCY:
                    sample_rate = int(round(_float(data, as_, ae)))
                elif aid == ID_CHANNELS:
                    channels = _uint(data, as_, ae)
    track_type = TRACK_TYPES.get(ttype, "other")
    if default_duration:
        frame_rate = 1_000_000_000 / default_duration
    return {
        "track_id": number,
        "type": track_type,
        "codec": CODECS.get(raw, f"unknown:{raw}"),
        "codec_raw": raw,
        "duration_ns": duration_ns,
        "width": width if track_type == "video" else None,
        "height": height if track_type == "video" else None,
        "frame_rate": frame_rate if track_type == "video" else None,
        "sample_rate": sample_rate,
        "channels": channels,
    }


def parse_mkv_bytes(data: bytes) -> list:
    """Parse Matroska metadata from *data* and return a list of track dicts."""
    segment = None
    for eid, s, e in _iter_elements(data, 0, len(data)):
        if eid == ID_SEGMENT:
            segment = (s, e)
            break
    if segment is None:
        raise MKVError("missing Segment element")
    info_range = tracks_range = None
    for eid, s, e in _iter_elements(data, *segment):
        if eid == ID_INFO:
            info_range = (s, e)
        elif eid == ID_TRACKS:
            tracks_range = (s, e)
    if tracks_range is None:
        raise MKVError("missing Tracks element")
    duration_ns = _parse_info(data, *info_range) if info_range else 0
    tracks = [
        _parse_track_entry(data, s, e, duration_ns)
        for eid, s, e in _iter_elements(data, *tracks_range)
        if eid == ID_TRACK_ENTRY
    ]
    if not tracks:
        raise MKVError("no track entries")
    return tracks


def parse_mkv(path: str) -> list:
    with open(path, "rb") as fh:
        return parse_mkv_bytes(fh.read())
