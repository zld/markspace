#!/usr/bin/env python3
"""Generate small synthetic MP4 fixtures without any external media.

Produces four files:
  master.mp4     video + dual audio + subtitle, extended-size mdat
  reordered.mp4  same tracks in a different order, zero-size (to-EOF) mdat
  drift.mp4      one audio track removed; another declares 4000 ms but its
                 sample table sums to 4250 ms (250 ms drift)
  truncated.mp4  moov box declares a size far beyond the actual file end
"""
from __future__ import annotations

import argparse
import struct
from pathlib import Path

MOVIE_TIMESCALE = 1000
MATRIX = (0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000)


def box(type_: bytes, payload: bytes) -> bytes:
    return struct.pack(">I4s", 8 + len(payload), type_) + payload


def box_extended(type_: bytes, payload: bytes) -> bytes:
    return struct.pack(">I4sQ", 1, type_, 16 + len(payload)) + payload


def box_to_eof(type_: bytes, payload: bytes) -> bytes:
    return struct.pack(">I4s", 0, type_) + payload


def fullbox(version: int, flags: int) -> bytes:
    return bytes([version]) + flags.to_bytes(3, "big")


def pack_language(code: str) -> int:
    bits = 0
    for char in code:
        bits = (bits << 5) | (ord(char) - 0x60)
    return bits


def make_ftyp() -> bytes:
    return box(b"ftyp", b"isom" + struct.pack(">I", 0x200) + b"isomiso2")


def make_mvhd() -> bytes:
    payload = fullbox(0, 0)
    payload += struct.pack(">IIII", 0, 0, MOVIE_TIMESCALE, 4000)
    payload += struct.pack(">IHH", 0x00010000, 0x0100, 0) + b"\0" * 8
    payload += struct.pack(">9I", *MATRIX) + b"\0" * 24
    payload += struct.pack(">I", 5)
    return box(b"mvhd", payload)


def make_tkhd(track_id: int, duration_ms: int, width: int, height: int,
              volume: int) -> bytes:
    payload = fullbox(0, 0x000003)
    payload += struct.pack(">IIIII", 0, 0, track_id, 0, duration_ms)
    payload += b"\0" * 8
    payload += struct.pack(">hhhh", 0, 0, volume, 0)
    payload += struct.pack(">9I", *MATRIX)
    payload += struct.pack(">II", width << 16, height << 16)
    return box(b"tkhd", payload)


def make_mdhd(timescale: int, duration: int, language: str) -> bytes:
    payload = fullbox(0, 0)
    payload += struct.pack(">IIII", 0, 0, timescale, duration)
    payload += struct.pack(">HH", pack_language(language), 0)
    return box(b"mdhd", payload)


def make_hdlr(handler: bytes, name: bytes) -> bytes:
    return box(b"hdlr", fullbox(0, 0) + b"\0" * 4 + handler + b"\0" * 12
               + name + b"\0")


def make_stsd(entry: bytes) -> bytes:
    return box(b"stsd", fullbox(0, 0) + struct.pack(">I", 1) + entry)


def video_sample_entry(width: int, height: int) -> bytes:
    body = b"\0" * 6 + struct.pack(">H", 1) + b"\0" * 16
    body += struct.pack(">HH", width, height)
    body += struct.pack(">II", 0x00480000, 0x00480000) + b"\0" * 4
    body += struct.pack(">H", 1) + b"\0" * 32
    body += struct.pack(">Hh", 0x0018, -1)
    return box(b"avc1", body)


def audio_sample_entry(channels: int, sample_rate: int) -> bytes:
    body = b"\0" * 6 + struct.pack(">H", 1) + b"\0" * 8
    body += struct.pack(">HHHHI", channels, 16, 0, 0, sample_rate << 16)
    return box(b"mp4a", body)


def text_sample_entry() -> bytes:
    return box(b"tx3g", b"\0" * 6 + struct.pack(">H", 1) + b"\0" * 8)


def make_stts(entries) -> bytes:
    payload = fullbox(0, 0) + struct.pack(">I", len(entries))
    for count, delta in entries:
        payload += struct.pack(">II", count, delta)
    return box(b"stts", payload)


def make_stbl(entry: bytes, stts_entries) -> bytes:
    stsc = box(b"stsc", fullbox(0, 0) + struct.pack(">IIII", 1, 1, 1, 1))
    stsz = box(b"stsz", fullbox(0, 0) + struct.pack(">II", 4, 1))
    stco = box(b"stco", fullbox(0, 0) + struct.pack(">II", 1, 0))
    return box(b"stbl", make_stsd(entry) + make_stts(stts_entries)
               + stsc + stsz + stco)


def make_minf(handler: bytes, entry: bytes, stts_entries) -> bytes:
    if handler == b"vide":
        header = box(b"vmhd", fullbox(0, 1) + b"\0" * 8)
    elif handler == b"soun":
        header = box(b"smhd", fullbox(0, 0) + b"\0" * 4)
    else:
        header = box(b"nmhd", fullbox(0, 0))
    dref = box(b"dref", fullbox(0, 0) + struct.pack(">I", 1)
               + box(b"url ", fullbox(0, 1)))
    return box(b"minf", header + box(b"dinf", dref)
               + make_stbl(entry, stts_entries))


def make_trak(track_id: int, handler: bytes, timescale: int,
              duration: int, language: str, entry: bytes,
              stts_entries, width: int = 0, height: int = 0) -> bytes:
    volume = 0x0100 if handler == b"soun" else 0
    name = {b"vide": b"VideoHandler", b"soun": b"SoundHandler"}.get(
        handler, b"TextHandler")
    mdia = box(b"mdia", make_mdhd(timescale, duration, language)
               + make_hdlr(handler, name)
               + make_minf(handler, entry, stts_entries))
    return box(b"trak", make_tkhd(track_id, 4000, width, height, volume)
               + mdia)


def track_video() -> bytes:
    return make_trak(1, b"vide", 90000, 360000, "und",
                     video_sample_entry(1920, 1080), [(100, 3600)],
                     1920, 1080)


def track_audio_en(stts_entries=((192, 1000),), duration=192000) -> bytes:
    return make_trak(2, b"soun", 48000, duration, "eng",
                     audio_sample_entry(2, 48000), stts_entries)


def track_audio_zh() -> bytes:
    return make_trak(3, b"soun", 48000, 192000, "zho",
                     audio_sample_entry(2, 48000), [(192, 1000)])


def track_subtitle() -> bytes:
    return make_trak(4, b"text", 1000, 4000, "eng",
                     text_sample_entry(), [(4, 1000)])


def make_moov(traks) -> bytes:
    return box(b"moov", make_mvhd() + b"".join(traks))


def build_master() -> bytes:
    moov = make_moov([track_video(), track_audio_en(), track_audio_zh(),
                      track_subtitle()])
    unknown = box(b"xtra", b"custom metadata block")
    return (make_ftyp() + moov + unknown
            + box_extended(b"mdat", b"\0" * 64))


def build_reordered() -> bytes:
    moov = make_moov([track_audio_zh(), track_subtitle(), track_video(),
                      track_audio_en()])
    return make_ftyp() + moov + box_to_eof(b"mdat", b"\0" * 64)


def build_drift() -> bytes:
    # Declared duration stays 192000 (4000 ms) but the sample table sums
    # to 204000 (4250 ms); the zh audio track is dropped entirely.
    drifting = track_audio_en(stts_entries=((192, 1000), (12, 1000)))
    moov = make_moov([track_video(), drifting, track_subtitle()])
    return make_ftyp() + moov + box(b"mdat", b"\0" * 64)


def build_truncated() -> bytes:
    moov = make_moov([track_video()])
    declared = struct.pack(">I4s", len(moov) + 4096, b"moov")
    return make_ftyp() + declared + moov[8:8 + 96]


BUILDERS = {
    "master.mp4": build_master,
    "reordered.mp4": build_reordered,
    "drift.mp4": build_drift,
    "truncated.mp4": build_truncated,
}


def build_all(out_dir) -> dict:
    out = Path(out_dir)
    out.mkdir(parents=True, exist_ok=True)
    written = {}
    for name, builder in sorted(BUILDERS.items()):
        path = out / name
        path.write_bytes(builder())
        written[name] = path
    return written


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", required=True, help="output directory")
    args = parser.parse_args(argv)
    written = build_all(args.out)
    for name, path in sorted(written.items()):
        print(f"wrote {path} ({path.stat().st_size} bytes)")
    print(f"fixtures={len(written)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
