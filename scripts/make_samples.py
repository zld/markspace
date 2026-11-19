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


# ---------------------------------------------------------------------------
# MP4 assembly
# ---------------------------------------------------------------------------


def build_mp4(path: Path, *, movie_timescale: int, tracks: list[dict],
              mdat_size: int | None = None, brand: bytes = b"isom",
              edit_list: bool = False) -> None:
    """Write a minimal but structurally valid MP4.

    Each track dict supplies: kind ('video'|'audio'), timescale, duration,
    track_id, plus width/height or sample_rate/channels, and optionally
    start_time (edit list media_time, in track timescale units).

    ``edit_list`` writes a real edts/elst that shifts presentation later by
    ``start_time`` while keeping media_time at zero, matching how editors
    express a delayed track.
    """
    if mdat_size is None:
        # Roughly the payload a clip of this duration would carry.
        mdat_size = max(1024, min(64 * 1024,
                                  int(max(t["duration"] / t["timescale"]
                                          for t in tracks) * 4096)))
    ftyp = box(b"ftyp", brand + struct.pack(">I", 512) +
               b"isom" + b"iso2" + b"mp41")
    # mdat sits after ftyp + moov. moov size depends on its contents but not
    # on the chunk offset value width, so build once to measure, then patch
    # the offset into the single stco entry.
    def assemble(chunk_offset: int) -> bytes:
        traks = []
        for t in tracks:
            if t["kind"] == "video":
                entry = visual_entry(t["width"], t["height"])
                handler = b"vide"
                volume = 0
                w, h = t["width"], t["height"]
            else:
                entry = audio_entry(t["sample_rate"], t["channels"])
                handler = b"soun"
                volume = 0x0100
                w = h = 0
            stbl = sample_table(entry, sample_count=1,
                                sample_size=min(mdat_size, 1024),
                                chunk_offset=chunk_offset)
            minf = box(b"minf",
                       full_box(b"vmhd" if handler == b"vide" else b"smhd",
                                0, 1, b"\x00" * (8 if handler == b"vide" else 4))
                       + dinf() + stbl)
            mdia = box(b"mdia", mdhd(t["timescale"], t["duration"],
                                     t.get("language", "und")) +
                       hdlr(handler, "VideoHandler" if handler == b"vide"
                            else "SoundHandler") + minf)
            header = tkhd(t["track_id"], 0, w, h, volume)
            if edit_list and t.get("start_time"):
                header += elst(0, t["duration"], movie_timescale,
                               shift=t["start_time"])
            traks.append(box(b"trak", header + mdia))
        movie_duration = max(
            int(round(movie_timescale * t["duration"] / t["timescale"]))
            for t in tracks)
        return box(b"moov", mvhd(movie_timescale, movie_duration)
                   + b"".join(traks))

    probe = assemble(0)
    mdat_offset = len(ftyp) + len(probe) + 8
    moov = assemble(mdat_offset)
    if len(moov) != len(probe):  # offsets are fixed width; this must hold
        raise AssertionError("moov size changed when patching chunk offset")
    mdat = box(b"mdat", mdat_payload(mdat_size, len(tracks)))
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(ftyp + moov + mdat)


def riff_chunk(fourcc: bytes, payload: bytes) -> bytes:
    pad = b"\x00" if len(payload) & 1 else b""
    return fourcc + struct.pack("<I", len(payload)) + payload + pad


def build_wav(path: Path, *, sample_rate: int, channels: int, bits: int,
              seconds: float, tag: int = 1) -> None:
    block_align = channels * bits // 8
    frames = int(round(seconds * sample_rate))
    data = bytes(frames * block_align)
    fmt = struct.pack("<HHIIHH", tag, channels, sample_rate,
                      sample_rate * block_align, block_align, bits)
    chunks = riff_chunk(b"fmt ", fmt) + riff_chunk(b"data", data)
    riff = b"RIFF" + struct.pack("<I", 4 + len(chunks)) + b"WAVE" + chunks
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(riff)


# ---------------------------------------------------------------------------
# Fixture set
# ---------------------------------------------------------------------------


def build_default_samples(out: Path) -> list[Path]:
    """Write the default fixture set and return the paths in stable order."""
    out.mkdir(parents=True, exist_ok=True)
    created: list[Path] = []

    level_wav = out / "level_intro_vo.wav"
    build_wav(level_wav, sample_rate=48000, channels=1, bits=16, seconds=2.0)
    created.append(level_wav)

    cutscene = out / "cutscene_01.mp4"
    build_mp4(cutscene, movie_timescale=1000, tracks=[
        {"kind": "video", "timescale": 30000, "duration": 60000,
         "track_id": 1, "width": 1920, "height": 1080},
        {"kind": "audio", "timescale": 48000, "duration": 96000,
         "track_id": 2, "sample_rate": 48000, "channels": 2},
    ])
    created.append(cutscene)

    dialogue = out / "dialogue_scene2.mp4"
    build_mp4(dialogue, movie_timescale=600, tracks=[
        {"kind": "video", "timescale": 24000, "duration": 48000,
         "track_id": 3, "width": 1280, "height": 720},
        {"kind": "audio", "timescale": 44100, "duration": 88200,
         "track_id": 4, "sample_rate": 44100, "channels": 1},
    ])
    created.append(dialogue)

    broken = out / "corrupt_truncated.mp4"
    data = cutscene.read_bytes()
    # Cut mid-file so the top-level mdat box declares a size the file cannot
    # satisfy; the parser must locate the overflow instead of reading past EOF.
    broken.write_bytes(data[: len(data) // 3])
    created.append(broken)
    return created


def build_edge_samples(out: Path) -> list[Path]:
    """Extra fixtures used by tests for comparison and error paths."""
    out.mkdir(parents=True, exist_ok=True)
    created: list[Path] = []

    offset = out / "offset_start.mp4"
    build_mp4(offset, movie_timescale=1000, edit_list=True, tracks=[
        {"kind": "video", "timescale": 30000, "duration": 60000,
         "track_id": 9, "width": 1920, "height": 1080, "start_time": 1000},
    ])
    created.append(offset)

    no_video = out / "audio_only.mp4"
    build_mp4(no_video, movie_timescale=1000, tracks=[
        {"kind": "audio", "timescale": 48000, "duration": 96000,
         "track_id": 2, "sample_rate": 48000, "channels": 2},
    ])
    created.append(no_video)

    # Deliberately out of delivery sync: audio runs 0.4s longer than video.
    drifted = out / "av_drift.mp4"
    build_mp4(drifted, movie_timescale=1000, tracks=[
        {"kind": "video", "timescale": 30000, "duration": 30000,
         "track_id": 1, "width": 1920, "height": 1080},
        {"kind": "audio", "timescale": 48000, "duration": 48000 + 19200,
         "track_id": 2, "sample_rate": 48000, "channels": 2},
    ])
    created.append(drifted)

    return created


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Generate local media fixtures")
    parser.add_argument("--out", required=True, type=Path,
                        help="directory to write fixture files into")
    parser.add_argument("--edge", action="store_true",
                        help="also write comparison-oriented edge fixtures")
    args = parser.parse_args(argv)
    created = build_default_samples(args.out)
    if args.edge:
        created += build_edge_samples(args.out)
    for path in created:
        print(f"{path} ({path.stat().st_size} bytes)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
