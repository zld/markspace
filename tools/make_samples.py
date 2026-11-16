"""Deterministic local sample generator for the media-audit test suite.

Running this script never reads external media: every byte of every sample is
synthesised here. The output set covers multi-track files, missing language
tags, duplicated track numbers, a duration conflict and truncated containers
in both ISO BMFF and Matroska flavours.
"""

from __future__ import annotations

import argparse
import json
import os
import struct
from typing import Iterable

# --------------------------------------------------------------------------
# ISO BMFF builders
# --------------------------------------------------------------------------


def box(box_type: bytes, payload: bytes) -> bytes:
    return struct.pack(">I4s", 8 + len(payload), box_type) + payload


def fullbox(box_type: bytes, version: int, flags: int, payload: bytes) -> bytes:
    return box(
        box_type, struct.pack(">I", (version << 24) | flags) + payload
    )


def pack_iso_language(language: str) -> int:
    value = 0
    for char in language[:3]:
        value = (value << 5) | (ord(char) - ord("a") + 1)
    return value


def ftyp(brand: bytes = b"isom") -> bytes:
    return box(
        b"ftyp",
        brand + struct.pack(">I", 0x200) + brand + b"iso2" + b"mp41",
    )


def mvhd(duration: int, timescale: int = 1000) -> bytes:
    body = bytes(8)  # creation / modification time
    body += struct.pack(">II", timescale, duration)
    body += struct.pack(">I", 0x00010000)  # rate 1.0
    body += struct.pack(">H", 0x0100)  # volume 1.0
    body += b"\x00" * 10  # reserved
    body += struct.pack(
        ">9I", 0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000
    )  # identity matrix
    body += b"\x00" * 24  # pre_defined
    body += struct.pack(">I", 3)  # next_track_id
    return fullbox(b"mvhd", 0, 0, body)


def tkhd(track_id: int, enabled: bool = True,
         width: int = 0, height: int = 0) -> bytes:
    flags = 0x000001 if enabled else 0x000000
    body = bytes(8)
    body += struct.pack(">I", track_id)
    body += struct.pack(">I", 0)  # reserved
    body += struct.pack(">I", 0)  # duration (unknown is acceptable)
    body += b"\x00" * 8  # reserved
    body += struct.pack(">Hh", 0, 0)  # layer, alternate group
    body += struct.pack(">H", 0x0100)  # volume
    body += struct.pack(">H", 0)  # reserved
    body += struct.pack(
        ">9I", 0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000
    )
    body += struct.pack(">II", width << 16, height << 16)
    return fullbox(b"tkhd", 0, flags, body)


def mdhd(timescale: int, duration: int, language: str | None) -> bytes:
    language_raw = (
        pack_iso_language(language) if language else 0x55C0
    )  # 0x55C0 = "und"
    body = bytes(8)
    body += struct.pack(">II", timescale, duration)
    body += struct.pack(">HH", language_raw, 0)
    return fullbox(b"mdhd", 0, 0, body)


def hdlr(handler_type: bytes, name: bytes = b"") -> bytes:
    body = bytes(4) + handler_type + b"\x00" * 12 + name + b"\x00"
    return fullbox(b"hdlr", 0, 0, body)


def video_entry(codec: bytes, width: int, height: int) -> bytes:
    body = b"\x00" * 6 + struct.pack(">H", 1)  # reserved + data ref index
    body += struct.pack(">H", 0) + b"\x00" * 6  # pre_defined + reserved
    body += struct.pack(">Hh", width, height)
    body += struct.pack(">II", 0x00480000, 0x00480000)  # 72 dpi
    body += struct.pack(">I", 0)  # reserved
    body += struct.pack(">H", 1)  # frame count
    body += b"\x00" * 32  # compressor name
    body += struct.pack(">Hh", 24, -1)  # depth + pre_defined
    return box(codec, body)


def audio_entry(codec: bytes, channels: int, sample_rate: int) -> bytes:
    body = b"\x00" * 6 + struct.pack(">H", 1)
    body += b"\x00" * 8  # version / revision / vendor
    body += struct.pack(">H", channels)
    body += struct.pack(">H", 16)  # sample size
    body += struct.pack(">HH", 0, 0)  # pre_defined + reserved
    body += struct.pack(">I", sample_rate << 16)  # 16.16 fixed point
    return box(codec, body)


def subtitle_entry(codec: bytes) -> bytes:
    body = b"\x00" * 6 + struct.pack(">H", 1)
    return box(codec, body)


def stsd(entry: bytes) -> bytes:
    return fullbox(b"stsd", 0, 0, struct.pack(">I", 1) + entry)


def trak(
    track_id: int,
    handler_type: bytes,
    language: str | None,
    entry: bytes,
    enabled: bool = True,
    width: int = 0,
    height: int = 0,
    duration: int = 0,
    timescale: int = 1000,
) -> bytes:
    content = tkhd(track_id, enabled, width, height)
    media = mdhd(timescale, duration, language) + hdlr(handler_type)
    media += box(b"minf", box(b"stbl", stsd(entry)))
    content += box(b"mdia", media)
    return box(b"trak", content)


def build_iso(
    brand: bytes,
    duration: int,
    trak_payloads: Iterable[bytes],
    extended_size: bool = False,
) -> bytes:
    moov = box(b"moov", mvhd(duration) + b"".join(trak_payloads))
    ftyp_box = ftyp(brand)
    if extended_size:
        # Re-emit ftyp with a 64-bit extended size (size32 == 1) to exercise
        # the largesize header path; only the original payload is re-wrapped.
        payload = ftyp_box[8:]
        ftyp_box = (
            struct.pack(">I4sQ", 1, b"ftyp", 16 + len(payload)) + payload
        )
    return ftyp_box + moov

