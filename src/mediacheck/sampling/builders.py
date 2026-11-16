"""Minimal, dependency-free MP4 and Matroska byte builders.

These builders emit only the structural metadata the mediacheck parsers
understand plus tiny placeholder payloads; they exist solely to create
deterministic offline test fixtures and deliberately support injecting
structural damage (truncation, missing tracks).
"""

from __future__ import annotations

import struct

# ---------------------------------------------------------------------------
# MP4 / ISO base media boxes
# ---------------------------------------------------------------------------


def box(kind: bytes, payload: bytes) -> bytes:
    return struct.pack(">I", 8 + len(payload)) + kind + payload


def full_box(kind: bytes, payload: bytes, version: int = 0,
             flags: int = 0) -> bytes:
    return box(kind, struct.pack(">I", (version << 24) | flags) + payload)


def _mvhd(timescale: int, duration: int) -> bytes:
    payload = struct.pack(">II", 0, 0)          # creation/modification time
    payload += struct.pack(">I", timescale)
    payload += struct.pack(">I", duration)
    payload += struct.pack(">I", 0x00010000)    # rate 1.0
    payload += struct.pack(">H", 0x0100)        # volume 1.0
    payload += b"\x00" * 10                     # reserved
    payload += struct.pack(">9I", 0x00010000, 0, 0, 0, 0x00010000, 0,
                           0, 0, 0x40000000)    # matrix
    payload += b"\x00" * 24                     # pre-defined
    payload += struct.pack(">I", 2)             # next track id
    return full_box(b"mvhd", payload)


def _tkhd(track_id: int, duration: int) -> bytes:
    payload = struct.pack(">II", 0, 0)
    payload += struct.pack(">I", track_id)
    payload += struct.pack(">I", 0)             # reserved
    payload += struct.pack(">I", duration)
    payload += b"\x00" * 8                      # reserved
    payload += struct.pack(">HH", 0, 0)         # layer/alt group
    payload += struct.pack(">H", 0)             # volume
    payload += struct.pack(">H", 0)             # reserved
    payload += struct.pack(">9I", 0x00010000, 0, 0, 0, 0x00010000, 0,
                           0, 0, 0x40000000)
    payload += struct.pack(">II", 0, 0)         # width/height (track-level)
    return full_box(b"tkhd", payload)


def _mdhd(timescale: int, duration: int) -> bytes:
    payload = struct.pack(">II", 0, 0)
    payload += struct.pack(">I", timescale)
    payload += struct.pack(">I", duration)
    payload += struct.pack(">H", 0x55C4)        # language 'und'
    payload += struct.pack(">H", 0)             # quality
    return full_box(b"mdhd", payload)


def _hdlr(handler: bytes) -> bytes:
    payload = struct.pack(">I", 0)              # pre-defined
    payload += handler
    payload += b"\x00" * 12                     # reserved
    payload += b"handler\x00"
    return full_box(b"hdlr", payload)


def _video_entry(codec: bytes, width: int, height: int) -> bytes:
    body = b"\x00" * 6                          # reserved
    body += struct.pack(">H", 1)                # data reference index
    body += struct.pack(">HH", 0, 0)            # pre-defined/reserved
    body += b"\x00" * 12                        # pre-defined
    body += struct.pack(">HH", width, height)
    body += struct.pack(">II", 0x00480000, 0x00480000)  # 72 dpi
    body += struct.pack(">I", 0)                # reserved
    body += struct.pack(">H", 1)                # frame count
    body += b"\x00" * 32                        # compressor name
    body += struct.pack(">H", 24)               # depth
    body += struct.pack(">h", -1)               # pre-defined
    return box(codec, body)


def _audio_entry(codec: bytes, rate: int, channels: int) -> bytes:
    body = b"\x00" * 6                    # reserved
    body += struct.pack(">H", 1)          # data reference index (offset 6)
    body += b"\x00" * 8                   # version/revision/vendor (8..15)
    body += struct.pack(">H", channels)   # channels at offset 16
    body += struct.pack(">H", 16)         # sample size at offset 18
    body += struct.pack(">HH", 0, 0)      # pre-defined / reserved
    body += struct.pack(">I", rate << 16)  # sample rate 16.16 at offset 24
    return box(codec, body)


def _stsd(entry: bytes) -> bytes:
    payload = struct.pack(">I", 1) + entry
    return full_box(b"stsd", payload)


def _minimal_stbl(entry: bytes) -> bytes:
    stsd = _stsd(entry)
    # Empty table boxes with zero entries still satisfy the parser, which
    # only reads metadata headers and never walks sample tables.
    return box(b"stbl", stsd
               + full_box(b"stts", struct.pack(">I", 0))
               + full_box(b"stsc", struct.pack(">I", 0))
               + full_box(b"stsz", struct.pack(">II", 0, 0))
               + full_box(b"stco", struct.pack(">I", 0)))


def _trak(track_id: int, handler: bytes, codec_entry: bytes,
          timescale: int, duration: int) -> bytes:
    tkhd = _tkhd(track_id, duration)
    mdia = box(b"mdia",
               _mdhd(timescale, duration)
               + _hdlr(handler)
               + box(b"minf", _minimal_stbl(codec_entry)))
    return box(b"trak", tkhd + mdia)


def build_mp4(timescale: int, duration: int,
              tracks: list[dict]) -> bytes:
    """Build a small MP4: ftyp + moov (metadata first) + trailing mdat."""
    ftyp = box(b"ftyp", b"isom" + struct.pack(">I", 0x200) + b"isommp41")
    children = _mvhd(timescale, duration)
    for index, spec in enumerate(tracks, start=1):
        if spec["type"] == "video":
            entry = _video_entry(spec["codec"].encode("latin-1"),
                                 spec["width"], spec["height"])
            handler = b"vide"
        else:
            entry = _audio_entry(spec["codec"].encode("latin-1"),
                                 spec["sample_rate"],
                                 spec.get("channels", 2))
            handler = b"soun"
        children += _trak(index, handler, entry, timescale, duration)
    moov = box(b"moov", children)
    mdat = box(b"mdat", b"\x00" * 32)
    return ftyp + moov + mdat


# ---------------------------------------------------------------------------
# Matroska / EBML
# ---------------------------------------------------------------------------


def encode_vint(value: int, width: int) -> bytes:
    """Encode a size VINT with an explicit byte width (1..8)."""
    if not 1 <= width <= 8:
        raise ValueError("VINT width out of range")
    marker = 1 << (7 * width)
    raw = marker | value
    return raw.to_bytes(width, "big")


def encode_vint_auto(value: int) -> bytes:
    for width in range(1, 9):
        if value < (1 << (7 * width)) - 1:
            return encode_vint(value, width)
    raise ValueError("value too large for VINT")


def encode_ebml_id(eid: int) -> bytes:
    """Element IDs carry their own length marker; encode raw width."""
    length = max(1, (eid.bit_length() + 7) // 8)
    return eid.to_bytes(length, "big")


def elem(eid: int, payload: bytes) -> bytes:
    return encode_ebml_id(eid) + encode_vint_auto(len(payload)) + payload


def elem_uint(eid: int, value: int) -> bytes:
    if value == 0:
        body = b""
    else:
        body = value.to_bytes(max(1, (value.bit_length() + 7) // 8), "big")
    return elem(eid, body)


def elem_float(eid: int, value: float) -> bytes:
    return elem(eid, struct.pack(">d", value))


def elem_string(eid: int, value: str) -> bytes:
    return elem(eid, value.encode("latin-1"))


# Element IDs mirrored from formats.py (kept local for fixture independence).
E_HEADER = 0x1A45DFA3
E_SEGMENT = 0x18538067
E_INFO = 0x1549A966
E_TRACKS = 0x1654AE6B
E_TRACK_ENTRY = 0xAE
E_TRACK_NUMBER = 0xD7
E_TRACK_UID = 0x73C5
E_TRACK_TYPE = 0x83
E_CODEC_ID = 0x86
E_VIDEO = 0xE0
E_AUDIO = 0xE1
E_PIXEL_WIDTH = 0xB0
E_PIXEL_HEIGHT = 0xBA
E_SAMPLING_FREQ = 0xB5
E_CHANNELS = 0x9F
E_TIMECODE_SCALE = 0x2AD7B1
E_DURATION = 0x4489
E_DOC_TYPE = 0x4282
E_DOC_TYPE_VERSION = 0x4287
E_DOC_TYPE_READ_VERSION = 0x4285
E_EBML_READ_VERSION = 0x42F7
E_EBML_VERSION = 0x4286

_TYPE_CODES = {"video": 1, "audio": 2, "subtitle": 17}


def _ebml_header() -> bytes:
    payload = elem_uint(E_EBML_VERSION, 1)
    payload += elem_uint(E_EBML_READ_VERSION, 1)
    payload += elem_uint(E_DOC_TYPE_VERSION, 4)
    payload += elem_uint(E_DOC_TYPE_READ_VERSION, 2)
    payload += elem_string(E_DOC_TYPE, "matroska")
    return elem(E_HEADER, payload)


def _track_entry(spec: dict, index: int) -> bytes:
    payload = elem_uint(E_TRACK_NUMBER, index)
    payload += elem_uint(E_TRACK_UID, 1000 + index)
    payload += elem_uint(E_TRACK_TYPE, _TYPE_CODES[spec["type"]])
    payload += elem_string(E_CODEC_ID, spec["codec_id"])
    if spec["type"] == "video":
        video = (elem_uint(E_PIXEL_WIDTH, spec["width"])
                 + elem_uint(E_PIXEL_HEIGHT, spec["height"]))
        payload += elem(E_VIDEO, video)
    elif spec["type"] == "audio":
        audio = (elem_float(E_SAMPLING_FREQ, float(spec["sample_rate"]))
                 + elem_uint(E_CHANNELS, spec.get("channels", 2)))
        payload += elem(E_AUDIO, audio)
    return elem(E_TRACK_ENTRY, payload)


def build_matroska(timescale_ns: int, duration_ticks: float,
                   tracks: list[dict]) -> bytes:
    """Build a minimal Matroska file (EBML header + one segment)."""
    info = elem(E_INFO,
                elem_uint(E_TIMECODE_SCALE, timescale_ns)
                + elem_float(E_DURATION, duration_ticks))
    track_entries = b"".join(
        _track_entry(spec, i) for i, spec in enumerate(tracks, start=1))
    segment_body = info + elem(E_TRACKS, track_entries)
    segment = elem(E_SEGMENT, segment_body)
    return _ebml_header() + segment
