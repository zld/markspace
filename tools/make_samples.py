#!/usr/bin/env python3
"""Generate deterministic local sample media and a matching baseline.

Usage::

    .venv/bin/python tools/make_samples.py --out .tmp/samples

Produces four valid files (7 tracks total) plus one intentionally corrupted
MP4, and ``reference.json`` used by ``mediascan check --baseline``.  Every byte
is synthesised here; no external media or network access is involved.
"""

from __future__ import annotations

import argparse
import json
import os
import struct
import sys

# -- tiny container builders -------------------------------------------------


def box(btype: str, payload: bytes) -> bytes:
    return struct.pack(">I", len(payload) + 8) + btype.encode("latin-1") + payload


def fullbox(version: int = 0, flags: int = 0) -> bytes:
    return bytes([version]) + flags.to_bytes(3, "big")


def ftyp(major: bytes = b"isom", brands: tuple[bytes, ...] = (b"isom", b"mp42")) -> bytes:
    payload = major + struct.pack(">I", 512) + b"".join(brands)
    return box("ftyp", payload)


def mvhd(timescale: int, duration: int) -> bytes:
    payload = (
        fullbox()
        + struct.pack(">IIII", 0, 0, timescale, duration)
        + struct.pack(">I", 0x00010000)  # rate 1.0
        + struct.pack(">H", 0x0100)  # volume 1.0
        + b"\x00" * 10
        + _unity_matrix()
        + b"\x00" * 24
        + struct.pack(">I", 100)
    )
    return box("mvhd", payload)


def _unity_matrix() -> bytes:
    return struct.pack(
        ">9I",
        0x00010000, 0, 0,
        0, 0x00010000, 0,
        0, 0, 0x40000000,
    )


def tkhd(track_id: int, duration: int, width: int = 0, height: int = 0) -> bytes:
    payload = (
        fullbox(flags=7)
        + struct.pack(">II", 0, 0)
        + struct.pack(">I", track_id)
        + struct.pack(">I", 0)
        + struct.pack(">I", duration)
        + b"\x00" * 8
        + struct.pack(">hhhh", 0, 0, 0x0100 if not width else 0, 0)
        + _unity_matrix()
        + struct.pack(">II", width << 16, height << 16)
    )
    return box("tkhd", payload)


def mdhd(timescale: int, duration: int) -> bytes:
    payload = (
        fullbox()
        + struct.pack(">II", 0, 0)
        + struct.pack(">II", timescale, duration)
        + struct.pack(">HH", 0x55C4, 0)  # language "und"
    )
    return box("mdhd", payload)


def hdlr(handler: bytes, name: str) -> bytes:
    payload = (
        fullbox()
        + struct.pack(">I", 0)
        + handler.ljust(4, b" ")
        + b"\x00" * 12
        + name.encode("utf-8") + b"\x00"
    )
    return box("hdlr", payload)


def stsd(entries: list[bytes]) -> bytes:
    payload = fullbox() + struct.pack(">I", len(entries)) + b"".join(entries)
    return box("stsd", payload)


def stts(entry_count: int = 1) -> bytes:
    payload = fullbox() + struct.pack(">I", 1) + struct.pack(">II", entry_count, 1024)
    return box("stts", payload)


def stsz(sample_size: int = 1024, count: int = 1) -> bytes:
    payload = fullbox() + struct.pack(">II", sample_size, count)
    return box("stsz", payload)


def stco(offset: int = 0) -> bytes:
    payload = fullbox() + struct.pack(">I", 1) + struct.pack(">I", offset)
    return box("stco", payload)


def video_entry(fourcc: bytes, width: int, height: int) -> bytes:
    body = (
        b"\x00" * 6
        + struct.pack(">H", 1)  # data reference index
        + b"\x00" * 16  # pre_defined / reserved
        + struct.pack(">HH", width, height)
        + struct.pack(">II", 0x00480000, 0x00480000)  # 72 dpi
        + struct.pack(">I", 0)
        + struct.pack(">H", 1)  # frame_count
        + b"\x00" * 32  # compressor name
        + struct.pack(">Hh", 0x0018, -1)
    )
    return struct.pack(">I", len(body) + 8) + fourcc + body


def text_entry(fourcc: bytes) -> bytes:
    body = b"\x00" * 6 + struct.pack(">H", 1) + b"\x00" * 8
    return struct.pack(">I", len(body) + 8) + fourcc + body


def descriptor(tag: int, payload: bytes) -> bytes:
    length = len(payload)
    encoded = bytes([0x80 | (length & 0x7F)]) if length > 0x7F else bytes([length])
    return bytes([tag]) + encoded + payload


def esds(object_type: int, asc: bytes) -> bytes:
    dec_specific = descriptor(0x05, asc)
    dec_config = descriptor(
        0x04,
        bytes([object_type, 0x15])
        + b"\x00\x00\x00"
        + struct.pack(">I", 128000)
        + struct.pack(">I", 128000)
        + dec_specific,
    )
    es = descriptor(0x03, struct.pack(">H", 1) + b"\x00" + dec_config)
    return box("esds", fullbox() + es)


def audio_entry(
    fourcc: bytes,
    channels: int,
    sample_rate: int,
    sample_size: int,
    extra: bytes = b"",
) -> bytes:
    body = (
        b"\x00" * 6
        + struct.pack(">H", 1)
        + struct.pack(">HHI", 0, 0, 0)
        + struct.pack(">HH", channels, sample_size)
        + struct.pack(">HH", 0, 0)
        + struct.pack(">I", sample_rate << 16)
    )
    return struct.pack(">I", len(body) + 8 + len(extra)) + fourcc + body + extra


def aac_asc(sample_rate_index: int, channels: int) -> bytes:
    value = (2 << 11) | (sample_rate_index << 7) | (channels << 3)
    return struct.pack(">H", value)


def trak(
    track_id: int,
    handler: bytes,
    timescale: int,
    duration: int,
    entry: bytes,
    width: int = 0,
    height: int = 0,
) -> bytes:
    stbl = box(
        "stbl",
        stsd([entry]) + stts() + stsz() + stco(),
    )
    minf = box("minf", stbl)
    mdia = box("mdia", mdhd(timescale, duration) + hdlr(handler, handler.decode()) + minf)
    return box("trak", tkhd(track_id, duration, width, height) + mdia)


def mp4(tracks: list[bytes], movie_duration: int) -> bytes:
    moov = box("moov", mvhd(1000, movie_duration) + b"".join(tracks))
    mdat = box("mdat", b"\x00" * 64)
    return ftyp() + moov + mdat


# -- audio builders ----------------------------------------------------------


def wav_bytes(sample_rate: int, channels: int, bits: int, frames: int) -> bytes:
    block_align = channels * bits // 8
    data = b"\x00" * (frames * block_align)
    fmt = struct.pack(
        "<HHIIHH",
        1,
        channels,
        sample_rate,
        sample_rate * block_align,
        block_align,
        bits,
    )
    body = b"WAVE" + b"fmt " + struct.pack("<I", len(fmt)) + fmt
    body += b"data" + struct.pack("<I", len(data)) + data
    return b"RIFF" + struct.pack("<I", len(body)) + body


def extended80(value: float) -> bytes:
    import math

    mant, exp = math.frexp(value)
    sign_exp = exp + 16383
    return struct.pack(">HQ", sign_exp, int(mant * (1 << 63)))


def aiff_bytes(sample_rate: int, channels: int, bits: int, frames: int) -> bytes:
    data = b"\x00" * (frames * channels * bits // 8)
    comm = struct.pack(">HIH", channels, frames, bits) + extended80(sample_rate)
    ssnd = struct.pack(">II", 0, 0) + data
    body = b"AIFF"
    body += b"COMM" + struct.pack(">I", len(comm)) + comm
    body += b"SSND" + struct.pack(">I", len(ssnd)) + ssnd
    return b"FORM" + struct.pack(">I", len(body)) + body


# -- sample definitions ------------------------------------------------------


def build_samples() -> dict[str, bytes]:
    video_a = trak(
        1,
        b"vide",
        90_000,
        180_000,
        video_entry(b"avc1", 1280, 720),
        width=1280,
        height=720,
    )
    audio_a = trak(
        2,
        b"soun",
        48_000,
        96_000,
        audio_entry(b"mp4a", 2, 48_000, 0, esds(0x40, aac_asc(3, 2))),
    )
    subtitle_a = trak(3, b"text", 1_000, 2_000, text_entry(b"tx3g"))
    scene_a = mp4([video_a, audio_a, subtitle_a], 2_000)

    video_b = trak(
        1,
        b"vide",
        60_000,  # timescale drift: baseline is 90000
        120_000,
        video_entry(b"avc1", 1280, 720),
        width=1280,
        height=720,
    )
    audio_b = trak(
        2,
        b"soun",
        48_000,
        101_760,  # 2120 ms: +120 ms against a 2000 ms baseline
        audio_entry(b"mp4a", 1, 48_000, 0, esds(0x40, aac_asc(3, 1))),
    )
    scene_b = mp4([video_b, audio_b], 2_120)

    voiceover = wav_bytes(48_000, 2, 16, 48_000)  # 1000 ms
    room_tone = aiff_bytes(44_100, 1, 16, 22_050)  # 500 ms

    broken = bytearray(
        mp4(
            [trak(1, b"soun", 48_000, 96_000,
                  audio_entry(b"mp4a", 2, 48_000, 0, esds(0x40, aac_asc(3, 2))))],
            2_000,
        )
    )
    # Corrupt the first trak box length so it overruns its parent (moov).
    trak_at = broken.find(b"trak") - 4
    broken[trak_at : trak_at + 4] = struct.pack(">I", 0x7FFF_FFF0)
    return {
        "scene_a.mp4": bytes(scene_a),
        "scene_b.mp4": bytes(scene_b),
        "voiceover.wav": voiceover,
        "room_tone.aiff": room_tone,
        "scene_broken.mp4": bytes(broken),
    }


def build_baseline() -> dict:
    return {
        "version": 1,
        "generated_by": "tools/make_samples.py",
        "note": "scene_b.mp4 intentionally drifts; scene_broken.mp4 is corrupt",
        "files": {
            "scene_a.mp4": {
                "duration_ms": 2000,
                "tracks": [
                    {
                        "type": "video",
                        "codec_summary": {"codec": "avc1", "width": 1280,
                                          "height": 720},
                        "duration_ms": 2000,
                        "timescale": 90000,
                    },
                    {
                        "type": "audio",
                        "codec_summary": {"codec": "aac", "sample_rate": 48000,
                                          "channels": 2, "bit_depth": None},
                        "duration_ms": 2000,
                        "timescale": 48000,
                    },
                    {
                        # codec intentionally absent in the sample: unknown,
                        # not guessed.
                        "type": "subtitle",
                        "codec_summary": {"codec": None},
                        "duration_ms": 2000,
                        "timescale": 1000,
                    },
                ],
            },
            "scene_b.mp4": {
                "duration_ms": 2000,
                "tracks": [
                    {
                        "type": "video",
                        "codec_summary": {"codec": "avc1", "width": 1280,
                                          "height": 720},
                        "duration_ms": 2000,
                        "timescale": 90000,
                    },
                    {
                        "type": "audio",
                        "codec_summary": {"codec": "aac", "sample_rate": 48000,
                                          "channels": 2, "bit_depth": None},
                        "duration_ms": 2000,
                        "timescale": 48000,
                    },
                ],
            },
            "voiceover.wav": {
                "duration_ms": 1000,
                "tracks": [
                    {
                        "type": "audio",
                        "codec_summary": {"codec": "pcm_s16le",
                                          "sample_rate": 48000, "channels": 2,
                                          "bit_depth": 16},
                        "duration_ms": 1000,
                        "timescale": 48000,
                    }
                ],
            },
            "room_tone.aiff": {
                "duration_ms": 500,
                "tracks": [
                    {
                        "type": "audio",
                        "codec_summary": {"codec": "pcm_s16be",
                                          "sample_rate": 44100, "channels": 1,
                                          "bit_depth": 16},
                        "duration_ms": 500,
                        "timescale": 44100,
                    }
                ],
            },
        },
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", required=True, help="output directory")
    args = parser.parse_args(argv)
    os.makedirs(args.out, exist_ok=True)
    for name, data in build_samples().items():
        path = os.path.join(args.out, name)
        with open(path, "wb") as fh:
            fh.write(data)
        print(f"wrote {path} ({len(data)} bytes)")
    baseline_path = os.path.join(args.out, "reference.json")
    with open(baseline_path, "w", encoding="utf-8") as fh:
        json.dump(build_baseline(), fh, indent=2)
        fh.write("\n")
    print(f"wrote {baseline_path}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
