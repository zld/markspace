#!/usr/bin/env python3
"""Generate tiny, self-contained MP4/MOV and Matroska fixtures.

Everything is written byte by byte from the standard library so the fixtures
are reproducible, reviewable and need no system encoder.

Normal fixtures
---------------
base.mp4       2 tracks (H.264 video, AAC audio), 1000 ms, timescale 600
candidate.mkv  2 tracks (H.264 video, Opus audio), 1012 ms, timescale 1 ms
corrupt_truncated.mp4  a valid MP4 cut mid-box
corrupt_size.mp4       a box whose declared size exceeds its parent
corrupt_unknown.bin    content the sniffer must reject
empty.mp4              zero bytes
"""

from __future__ import annotations

import argparse
import struct
import sys
from pathlib import Path

# -- ISO BMFF helpers ------------------------------------------------------

def box(type_: bytes, payload: bytes) -> bytes:
    return struct.pack(">I", len(payload) + 8) + type_ + payload


def fullbox(type_: bytes, version: int, flags: int, payload: bytes) -> bytes:
    return box(type_, struct.pack(">I", (version << 24) | flags) + payload)


def mvhd(timescale: int, duration: int) -> bytes:
    payload = struct.pack(">IIII", 0, 0, timescale, duration)
    payload += struct.pack(">I", 0x00010000)  # rate 1.0
    payload += struct.pack(">H", 0x0100)      # volume 1.0
    payload += b"\x00" * 10                   # reserved
    payload += struct.pack(">9i", 0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000)
    payload += b"\x00" * 24                   # pre_defined
    payload += struct.pack(">I", 3)           # next_track_id
    return fullbox(b"mvhd", 0, 0, payload)


def tkhd(track_id: int, duration: int, width: int = 0,
         height: int = 0) -> bytes:
    payload = struct.pack(">II", 0, 0)
    payload += struct.pack(">I", track_id)
    payload += struct.pack(">I", 0)           # reserved
    payload += struct.pack(">I", duration)
    payload += b"\x00" * 8                    # reserved
    payload += struct.pack(">hhhH", 0, 0, 0, 0)
    payload += struct.pack(">9i", 0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000)
    payload += struct.pack(">II", width << 16, height << 16)
    return fullbox(b"tkhd", 0, 0x000003, payload)


def mdhd(timescale: int, duration: int) -> bytes:
    payload = struct.pack(">IIII", 0, 0, timescale, duration)
    payload += struct.pack(">HH", 0x55C4, 0)  # language "und"
    return fullbox(b"mdhd", 0, 0, payload)


def hdlr(handler: bytes, name: str) -> bytes:
    payload = struct.pack(">I", 0) + handler + b"\x00" * 12
    payload += name.encode("utf-8") + b"\x00"
    return fullbox(b"hdlr", 0, 0, payload)


def visual_entry(codec: bytes, width: int, height: int) -> bytes:
    payload = b"\x00" * 6 + struct.pack(">H", 1)          # reserved, dref
    payload += b"\x00" * 16                                # pre_defined etc
    payload += struct.pack(">HH", width, height)
    payload += struct.pack(">II", 0x00480000, 0x00480000)  # 72 dpi
    payload += b"\x00" * 4                                 # reserved
    payload += struct.pack(">H", 1)                        # frame_count
    payload += b"\x00" * 32                                # compressor name
    payload += struct.pack(">H", 0x0018)                   # depth
    payload += struct.pack(">h", -1)                       # pre_defined
    return struct.pack(">I", len(payload) + 8) + codec + payload


def audio_entry(codec: bytes, channels: int, rate: int,
                sample_size: int = 16) -> bytes:
    payload = b"\x00" * 6 + struct.pack(">H", 1)          # reserved, dref
    payload += b"\x00" * 8                                 # version + reserved
    payload += struct.pack(">HH", channels, sample_size)
    payload += struct.pack(">HH", 0, 0)                    # pre_defined, reserved
    payload += struct.pack(">I", rate << 16)
    return struct.pack(">I", len(payload) + 8) + codec + payload


def stsd(entries: list[bytes]) -> bytes:
    return fullbox(b"stsd", 0, 0,
                   struct.pack(">I", len(entries)) + b"".join(entries))


def stbl(entry: bytes) -> bytes:
    stts = fullbox(b"stts", 0, 0, struct.pack(">II", 0, 0))
    stsc = fullbox(b"stsc", 0, 0, struct.pack(">I", 0))
    stsz = fullbox(b"stsz", 0, 0, struct.pack(">II", 0, 0))
    stco = fullbox(b"stco", 0, 0, struct.pack(">I", 0))
    return box(b"stbl", stsd([entry]) + stts + stsc + stsz + stco)


def trak(track_id: int, duration_movie: int, media_timescale: int,
         media_duration: int, handler: bytes, entry: bytes, title: str,
         width: int = 0, height: int = 0) -> bytes:
    body = tkhd(track_id, duration_movie, width, height)
    body += box(b"edts", fullbox(b"elst", 0, 0,
                                 struct.pack(">IIiI", 1, duration_movie, 0,
                                             0x00010000)))
    body += box(b"mdia",
                mdhd(media_timescale, media_duration)
                + hdlr(handler, title)
                + box(b"minf", stbl(entry)))
    return box(b"trak", body)


def mdat(payload: bytes) -> bytes:
    return box(b"mdat", payload)


def build_mp4(movie_timescale: int = 600, movie_duration: int = 600,
              video_codec: bytes = b"avc1",
              audio_codec: bytes = b"mp4a") -> bytes:
    ftyp = box(b"ftyp", b"isom" + struct.pack(">I", 512) + b"isomiso2avc1mp41")
    tracks = trak(1, movie_duration, 30000, 30000, b"vide",
                  visual_entry(video_codec, 320, 180), "VideoHandler",
                  320, 180)
    tracks += trak(2, movie_duration, 48000, 48000, b"soun",
                   audio_entry(audio_codec, 2, 48000), "SoundHandler")
    moov = box(b"moov", mvhd(movie_timescale, movie_duration) + tracks)
    return ftyp + mdat(b"\x00" * 64) + moov


# -- EBML helpers ----------------------------------------------------------

def vint_size(value: int) -> bytes:
    for length in range(1, 9):
        if value < (1 << (7 * length)) - 1:
            return ((1 << (7 * length)) | value).to_bytes(length, "big")
    raise ValueError("size too large")


def el(id_: bytes, payload: bytes) -> bytes:
    return id_ + vint_size(len(payload)) + payload


def el_uint(id_: bytes, value: int) -> bytes:
    width = max(1, (value.bit_length() + 7) // 8)
    return el(id_, value.to_bytes(width, "big"))


def el_str(id_: bytes, text: str) -> bytes:
    return el(id_, text.encode("utf-8"))


def el_float(id_: bytes, value: float) -> bytes:
    return el(id_, struct.pack(">d", value))


def ebml_header(doc_type: str = "matroska") -> bytes:
    payload = el_uint(b"\x42\x86", 1)          # EBMLVersion
    payload += el_uint(b"\x42\xF7", 1)         # EBMLReadVersion
    payload += el_uint(b"\x42\xF2", 4)         # EBMLMaxIDLength
    payload += el_uint(b"\x42\xF3", 8)         # EBMLMaxSizeLength
    payload += el_str(b"\x42\x82", doc_type)   # DocType
    payload += el_uint(b"\x42\x87", 4)         # DocTypeVersion
    payload += el_uint(b"\x42\x85", 2)         # DocTypeReadVersion
    return el(b"\x1a\x45\xdf\xa3", payload)


def mkv_track(number: int, track_type: int, codec_id: str,
              extra: bytes = b"") -> bytes:
    payload = el_uint(b"\xD7", number)
    payload += el_uint(b"\x73\xC5", number)
    payload += el_uint(b"\x83", track_type)
    payload += el_str(b"\x86", codec_id)
    payload += extra
    return el(b"\xAE", payload)


def video_extra(width: int, height: int) -> bytes:
    return el(b"\xE0", el_uint(b"\xB0", width) + el_uint(b"\xBA", height))


def audio_extra(rate: float, channels: int) -> bytes:
    return el(b"\xE1", el_float(b"\xB5", rate) + el_uint(b"\x9F", channels))


def mkv_block(track: int, rel_tc: int, payload: bytes = b"\x00" * 8) -> bytes:
    # track number as a 1-byte vint, 16-bit signed timecode, flags byte
    return el(b"\xA3", vint_size(track)[-1:] +
              struct.pack(">hB", rel_tc, 0x80) + payload)


def build_mkv(video_end_ms: int = 1012, audio_end_ms: int = 1012,
              video_codec: str = "V_MPEG4/ISO/AVC",
              audio_codec: str = "A_OPUS") -> bytes:
    tracks = el(b"\x16\x54\xAE\x6B",
                mkv_track(1, 1, video_codec, video_extra(320, 180))
                + mkv_track(2, 2, audio_codec, audio_extra(48000.0, 2)))
    duration = max(video_end_ms, audio_end_ms)
    info = el(b"\x15\x49\xA9\x66",
              el_uint(b"\x2A\xD7\xB1", 1_000_000) +
              el_float(b"\x44\x89", float(duration)))
    clusters = b""
    # One cluster at t=0 holds a block near the end of each track.
    block_payload = mkv_block(1, video_end_ms) + mkv_block(2, audio_end_ms)
    clusters = el(b"\x1F\x43\xB6\x75",
                  el_uint(b"\xE7", 0) + block_payload)
    segment = el(b"\x18\x53\x80\x67", info + tracks + clusters)
    return ebml_header() + segment


def build_mkv_unknown_size(duration_ms: int = 500) -> bytes:
    """Matroska with unknown-size Segment and Cluster markers.

    Both must be bounded by the reader: the segment by end-of-file and the
    cluster by the next segment-level element.
    """
    tracks = el(b"\x16\x54\xAE\x6B",
                mkv_track(1, 1, "V_MPEG4/ISO/AVC", video_extra(320, 180))
                + mkv_track(2, 2, "A_OPUS", audio_extra(48000.0, 2)))
    info = el(b"\x15\x49\xA9\x66",
              el_uint(b"\x2A\xD7\xB1", 1_000_000) +
              el_float(b"\x44\x89", float(duration_ms)))
    cluster = (b"\x1F\x43\xB6\x75\xFF" +
               el_uint(b"\xE7", 0) +
               mkv_block(1, duration_ms) + mkv_block(2, duration_ms))
    segment = b"\x18\x53\x80\x67\xFF" + info + tracks + cluster
    return ebml_header() + segment


# -- corruption helpers ----------------------------------------------------

def corrupt_size(data: bytes) -> bytes:
    """Rewrite the moov size to claim more bytes than the file holds."""
    marker = b"moov"
    index = data.find(marker)
    if index < 4:
        raise ValueError("fixture has no moov box to corrupt")
    declared = len(data) - (index - 4) + 4096
    return data[:index - 4] + struct.pack(">I", declared) + data[index:]


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", required=True, type=Path,
                        help="directory to write fixtures into")
    args = parser.parse_args(argv)
    out: Path = args.out
    out.mkdir(parents=True, exist_ok=True)

    base = build_mp4()
    (out / "base.mp4").write_bytes(base)
    (out / "candidate.mkv").write_bytes(build_mkv())
    (out / "unknown_size.mkv").write_bytes(build_mkv_unknown_size())
    (out / "mov_variant.mov").write_bytes(
        build_mp4(video_codec=b"avc1", audio_codec=b"mp4a"))
    (out / "truncated.mp4").write_bytes(base[:len(base) // 2])
    (out / "oversize.mp4").write_bytes(corrupt_size(base))
    (out / "unknown.bin").write_bytes(b"this is not a media container\n" * 4)
    (out / "empty.mp4").write_bytes(b"")
    (out / "audio_only.mkv").write_bytes(build_mkv(video_end_ms=0))

    for name in sorted(p.name for p in out.iterdir()):
        size = (out / name).stat().st_size
        print(f"wrote {name} ({size} bytes)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
