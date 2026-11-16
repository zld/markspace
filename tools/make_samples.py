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


# --------------------------------------------------------------------------
# Matroska EBML builders
# --------------------------------------------------------------------------


def _vint_width_for(value: int) -> int:
    for width in range(1, 9):
        if value < (1 << (7 * width)) - 1:
            return width
    raise ValueError(f"value {value} too large for an 8-byte VINT")


def _encode_vint(value: int, width: int) -> bytes:
    raw = value.to_bytes(width, "big")
    marker = 1 << (8 - width)
    return bytes([raw[0] | marker]) + raw[1:]


def _encode_id(element_id: int) -> bytes:
    # Matroska IDs already carry their marker bits; width is the minimal
    # byte length, not the data-magnitude rule used for sizes.
    width = max(1, (element_id.bit_length() + 7) // 8)
    return _encode_vint(element_id, width)


def elem(element_id: int, payload: bytes) -> bytes:
    id_bytes = _encode_id(element_id)
    size_bytes = _encode_vint(len(payload), _vint_width_for(len(payload)))
    return id_bytes + size_bytes + payload


def elem_uint(element_id: int, value: int) -> bytes:
    if value == 0:
        body = b""
    else:
        body = value.to_bytes((value.bit_length() + 7) // 8, "big")
    return elem(element_id, body)


def elem_float(element_id: int, value: float) -> bytes:
    return elem(element_id, struct.pack(">d", value))


def elem_string(element_id: int, value: str) -> bytes:
    return elem(element_id, value.encode("ascii"))


# Element IDs (marker bits retained, per Matroska convention).
E_EBML = 0x1A45DFA3
E_SEGMENT = 0x18538067
E_INFO = 0x1549A966
E_TRACKS = 0x1654AE6B
E_TRACK_ENTRY = 0xAE
E_TIMECODE_SCALE = 0x2AD7B1
E_DURATION = 0x4489
E_TRACK_NUMBER = 0xD7
E_TRACK_UID = 0x73C5
E_FLAG_HIDDEN = 0x7E7B
E_FLAG_ENABLED = 0xB9
E_LANGUAGE = 0x22B59C
E_CODEC_ID = 0x86
E_TRACK_TYPE = 0x83
E_VIDEO = 0xE0
E_AUDIO = 0xE1
E_WIDTH = 0xB0
E_HEIGHT = 0xBA
E_SAMPLE_RATE = 0xB5
E_CHANNELS = 0x9F


def ebml_header() -> bytes:
    payload = (
        elem_uint(0x4286, 1)  # EBMLVersion
        + elem_uint(0x42F7, 1)  # EBMLReadVersion
        + elem_uint(0x42F2, 4)  # EBMLMaxIDLength
        + elem_uint(0x42F3, 8)  # EBMLMaxSizeLength
        + elem_string(0x4282, "matroska")  # DocType
        + elem_uint(0x4287, 4)  # DocTypeVersion
        + elem_uint(0x4285, 2)  # DocTypeReadVersion
    )
    return elem(E_EBML, payload)


def mkv_track_entry(
    number: int,
    track_type: int,
    codec_id: str,
    language: str | None,
    hidden: bool = False,
    width: int | None = None,
    height: int | None = None,
    sample_rate: int | None = None,
    channels: int | None = None,
) -> bytes:
    payload = (
        elem_uint(E_TRACK_NUMBER, number)
        + elem_uint(E_TRACK_UID, 1000 + number)
        + elem_uint(E_FLAG_ENABLED, 0 if hidden else 1)
        + elem_uint(E_FLAG_HIDDEN, 1 if hidden else 0)
        + elem_string(E_CODEC_ID, codec_id)
        + elem_uint(E_TRACK_TYPE, track_type)
    )
    if language:
        payload += elem_string(E_LANGUAGE, language)
    if width is not None and height is not None:
        payload += elem(E_VIDEO, elem_uint(E_WIDTH, width)
                        + elem_uint(E_HEIGHT, height))
    if sample_rate is not None and channels is not None:
        payload += elem(
            E_AUDIO,
            elem_float(E_SAMPLE_RATE, float(sample_rate))
            + elem_uint(E_CHANNELS, channels),
        )
    return elem(E_TRACK_ENTRY, payload)


def build_mkv(duration_ms: int, timecode_scale: int,
              track_entries: list[bytes]) -> bytes:
    info = elem(
        E_INFO,
        elem_uint(E_TIMECODE_SCALE, timecode_scale)
        + elem_float(E_DURATION, duration_ms * 1e6 / timecode_scale),
    )
    tracks = elem(E_TRACKS, b"".join(track_entries))
    return ebml_header() + elem(E_SEGMENT, info + tracks)


# --------------------------------------------------------------------------
# Deterministic scenario assembly
# --------------------------------------------------------------------------


def sample_cutscene_a() -> bytes:
    tracks = [
        trak(1, b"vide", "eng", video_entry(b"avc1", 1920, 1080),
             width=1920, height=1080, duration=10000),
        trak(2, b"soun", "eng", audio_entry(b"mp4a", 2, 48000),
             duration=10000),
        trak(3, b"subt", "eng", subtitle_entry(b"tx3g"), duration=10000),
    ]
    # 64-bit extended-size ftyp exercises the largesize header path.
    return build_iso(b"isom", 10000, tracks, extended_size=True)


def sample_cutscene_b() -> bytes:
    # 120 ms longer than A; the subtitle track deliberately lacks a language.
    tracks = [
        trak(1, b"vide", "eng", video_entry(b"avc1", 1920, 1080),
             width=1920, height=1080, duration=10120),
        trak(2, b"soun", "eng", audio_entry(b"mp4a", 2, 48000),
             duration=10120),
        trak(3, b"subt", None, subtitle_entry(b"tx3g"), duration=10120),
    ]
    return build_iso(b"isom", 10120, tracks)


def sample_cutscene_c() -> bytes:
    # QuickTime container: both tracks reuse track id 1 (duplicate number).
    tracks = [
        trak(1, b"vide", "eng", video_entry(b"avc1", 1280, 720),
             width=1280, height=720, duration=5000),
        trak(1, b"soun", "eng", audio_entry(b"mp4a", 2, 44100),
             duration=5000),
    ]
    return build_iso(b"qt  ", 5000, tracks)


def sample_cutscene_d() -> bytes:
    entries = [
        mkv_track_entry(1, 1, "V_MPEG4/ISO/AVC", "eng",
                        width=1920, height=1080),
        mkv_track_entry(2, 2, "A_AAC", "eng",
                        sample_rate=48000, channels=2),
    ]
    return build_mkv(10000, 1_000_000, entries)


def sample_cutscene_e() -> bytes:
    # Audio track omits its language tag entirely.
    entries = [
        mkv_track_entry(4, 1, "V_MPEG4/ISO/AVC", "eng",
                        width=1280, height=720),
        mkv_track_entry(5, 2, "A_AAC", None,
                        sample_rate=44100, channels=2),
    ]
    return build_mkv(5000, 1_000_000, entries)


def sample_truncated() -> bytes:
    # A syntactically valid Matroska file cut inside its Tracks element so
    # the declared element size runs past the end of the file.
    entries = [
        mkv_track_entry(1, 1, "V_MPEG4/ISO/AVC", "eng",
                        width=1920, height=1080),
        mkv_track_entry(2, 2, "A_AAC", "eng",
                        sample_rate=48000, channels=2),
    ]
    valid = build_mkv(10000, 1_000_000, entries)
    return valid[: len(valid) - 40]


def sample_truncated_mp4() -> bytes:
    # A valid MP4 whose moov box header declares more bytes than exist.
    valid = sample_cutscene_b()
    return valid[: len(valid) - 30]


def sample_mov_zero_tail() -> bytes:
    # QuickTime file ending in a zero-size (extends-to-EOF) tail box.
    head = build_iso(
        b"qt  ", 5000,
        [trak(1, b"vide", "eng", video_entry(b"avc1", 640, 480),
              width=640, height=480, duration=5000)],
    )
    return head + struct.pack(">I4s", 0, b"skip") + b"tail-payload"


def build_manifest() -> dict:
    return {
        "version": 1,
        "groups": [
            {
                "name": "cutscene_main",
                "files": [
                    "cutscene_a.mp4",
                    "cutscene_b.mp4",
                    "cutscene_d.mkv",
                ],
                "expect": [
                    {"type": "video", "count": 1},
                    {"type": "audio", "count": 1},
                ],
            },
            {
                "name": "cutscene_alt",
                "files": ["cutscene_c.mov", "cutscene_e.mkv"],
                "expect": [
                    {"type": "video", "count": 1},
                    {"type": "audio", "count": 1},
                ],
            },
        ],
    }


SAMPLES = {
    "cutscene_a.mp4": sample_cutscene_a,
    "cutscene_b.mp4": sample_cutscene_b,
    "cutscene_c.mov": sample_cutscene_c,
    "cutscene_d.mkv": sample_cutscene_d,
    "cutscene_e.mkv": sample_cutscene_e,
    "cutscene_truncated.mkv": sample_truncated,
}


def main() -> int:
    parser = argparse.ArgumentParser(description="Generate deterministic samples")
    parser.add_argument("--out", required=True, help="output directory")
    args = parser.parse_args()

    out_dir = os.path.abspath(args.out)
    os.makedirs(out_dir, exist_ok=True)
    for name, builder in SAMPLES.items():
        target = os.path.join(out_dir, name)
        with open(target, "wb") as handle:
            handle.write(builder())
    # A non-media file proves unknown extensions are skipped, not parsed.
    with open(os.path.join(out_dir, "delivery_notes.txt"), "w",
              encoding="utf-8") as handle:
        handle.write("vendor delivery notes; not a media container\n")
    with open(os.path.join(out_dir, "manifest.json"), "w",
              encoding="utf-8") as handle:
        json.dump(build_manifest(), handle, indent=2, sort_keys=True)
        handle.write("\n")
    print(f"wrote {len(SAMPLES)} media samples + manifest to {out_dir}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
