#!/usr/bin/env python3
"""Generate local media fixtures for mediacheck.

Everything is synthesized in-process: no external tools and no network.
Run ``python scripts/make_samples.py --out .tmp/samples`` to recreate the
default fixture set. The MP4 writer emits a structurally valid, minimal
ISO BMFF file with real moov/trak/mdia/stbl hierarchy and a sample table
that points at a tiny mdat payload.
"""

from __future__ import annotations

import argparse
import struct
import sys
from pathlib import Path

# ---------------------------------------------------------------------------
# Box helpers
# ---------------------------------------------------------------------------


def box(fourcc: bytes, payload: bytes) -> bytes:
    return struct.pack(">I", 8 + len(payload)) + fourcc + payload


def full_box(fourcc: bytes, version: int, flags: int, payload: bytes) -> bytes:
    return box(fourcc, bytes([version]) + flags.to_bytes(3, "big") + payload)


def mvhd(timescale: int, duration: int) -> bytes:
    body = struct.pack(">IIII", 0, 0, timescale, duration)
    body += struct.pack(">I", 0x00010000)      # rate 1.0
    body += struct.pack(">H", 0x0100)          # volume 1.0
    body += b"\x00" * 10                       # reserved
    body += struct.pack(">9i", 0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000)
    body += b"\x00" * 24                       # pre_defined
    body += struct.pack(">I", 3)               # next_track_id
    return full_box(b"mvhd", 0, 0, body)


def tkhd(track_id: int, duration: int, width: int, height: int,
         volume: int = 0) -> bytes:
    body = struct.pack(">IIIII", 0, 0, track_id, 0, duration)
    body += b"\x00" * 8
    body += struct.pack(">hhH", 0, 0, volume)
    body += b"\x00\x00"
    body += struct.pack(">9i", 0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000)
    body += struct.pack(">II", width << 16, height << 16)
    return full_box(b"tkhd", 0, 3, body)  # flags 3: enabled + in_movie


def mdhd(timescale: int, duration: int, language: str = "und") -> bytes:
    packed = 0
    for ch in language:
        packed = (packed << 5) | (ord(ch) - 0x60)
    body = struct.pack(">IIIIHH", 0, 0, timescale, duration, packed, 0)
    return full_box(b"mdhd", 0, 0, body)


def hdlr(handler: bytes, name: str) -> bytes:
    body = b"\x00" * 4 + handler + b"\x00" * 12 + name.encode() + b"\x00"
    return full_box(b"hdlr", 0, 0, body)


def dinf() -> bytes:
    url = full_box(b"url ", 0, 1, b"")
    dref = full_box(b"dref", 0, 0, struct.pack(">I", 1) + url)
    return box(b"dinf", dref)


def sample_table(entry: bytes, sample_count: int, sample_size: int,
                 chunk_offset: int) -> bytes:
    stsd = full_box(b"stsd", 0, 0, struct.pack(">I", 1) + entry)
    stts = full_box(b"stts", 0, 0, struct.pack(">III", 1, sample_count, 1))
    stsc = full_box(b"stsc", 0, 0,
                    struct.pack(">IIII", 1, 1, sample_count, 1))
    stsz = full_box(b"stsz", 0, 0,
                    struct.pack(">II", sample_size, sample_count))
    stco = full_box(b"stco", 0, 0, struct.pack(">II", 1, chunk_offset))
    stbl = box(b"stbl", stsd + stts + stsc + stsz + stco)
    return stbl


def elst(media_time: int, segment_duration: int, movie_timescale: int,
         version: int = 0, shift: int = 0) -> bytes:
    """Build edts/elst. ``shift`` inserts an empty edit that delays the track."""
    if shift:
        if version == 1:
            empty = struct.pack(">Qqhh", shift, -1, 1, 0)
            media = struct.pack(">Qqhh", segment_duration, media_time, 1, 0)
            body = struct.pack(">I", 2) + empty + media
        else:
            empty = struct.pack(">Iihh", shift, -1, 1, 0)
            media = struct.pack(">Iihh", segment_duration, media_time, 1, 0)
            body = struct.pack(">I", 2) + empty + media
    elif version == 1:
        body = struct.pack(">I", 1) + struct.pack(
            ">Qqhh", segment_duration, media_time, 1, 0)
    else:
        body = struct.pack(">I", 1) + struct.pack(
            ">Iihh", segment_duration, media_time, 1, 0)
    return box(b"edts", full_box(b"elst", version, 0, body))


# ---------------------------------------------------------------------------
# Sample entries
# ---------------------------------------------------------------------------


def visual_entry(width: int, height: int, fourcc: bytes = b"avc1") -> bytes:
    # VisualSampleEntry fixed fields are 78 bytes before child boxes.
    body = b"\x00" * 6 + struct.pack(">H", 1)
    body += b"\x00" * 16
    body += struct.pack(">HH", width, height)
    body += struct.pack(">II", 0x00480000, 0x00480000)  # 72 dpi
    body += b"\x00" * 4
    body += struct.pack(">H", 1)          # frame_count
    body += b"\x00" * 32                  # compressorname (32 bytes)
    body += struct.pack(">Hh", 24, -1)    # depth, pre_defined
    child = box(b"avcC", b"\x01\x64\x00\x1f\xff\xe1\x00\x00\x00\x00\x01")
    payload = body + child
    return struct.pack(">I", 8 + len(payload)) + fourcc + payload


def audio_entry(sample_rate: int, channels: int, fourcc: bytes = b"mp4a",
                bitrate: int = 128000) -> bytes:
    body = b"\x00" * 6 + struct.pack(">H", 1)
    body += b"\x00" * 8
    body += struct.pack(">HH", channels, 16)
    body += struct.pack(">HH", 0, 0)
    body += struct.pack(">I", sample_rate << 16)
    # esds with objectTypeIndication 0x40 (AAC)
    dec_specific = b"\x05\x02\x12\x10"
    decoder_config = (b"\x04" + bytes([13 + len(dec_specific)]) +
                      bytes([0x40, 0x15]) +
                      b"\x00\x00\x00" +
                      struct.pack(">I", bitrate) + struct.pack(">I", bitrate) +
                      dec_specific)
    sl_config = b"\x06\x01\x02"
    es = b"\x03" + bytes([3 + len(decoder_config) + len(sl_config)]) + b"\x00\x01\x00" + decoder_config + sl_config
    payload = body + full_box(b"esds", 0, 0, es)
    return struct.pack(">I", 8 + len(payload)) + fourcc + payload


def mdat_payload(size: int, tag: int) -> bytes:
    seed = (tag * 2654435761) & 0xFFFFFFFF
    out = bytearray()
    while len(out) < size:
        seed = (seed * 1103515245 + 12345) & 0xFFFFFFFF
        out += seed.to_bytes(4, "little")
    return bytes(out[:size])
