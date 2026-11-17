#!/usr/bin/env python3
"""Generate deterministic sample media and a manifest for mediaaudit.

The MP4 and Matroska writers below emit the minimal metadata subset the
auditor parses; no external media tools are involved and the output is
byte-for-byte reproducible. With --inject-drift the preview video track
runs 17 ms long and the diagnostic audio track drops to 44.1 kHz.
"""
from __future__ import annotations

import argparse
import json
import struct
from pathlib import Path

SHOW_ID = "SHOW-001"
FILES = ["master.mp4", "preview.mp4", "diagnostic.mkv", "review.mkv"]

# ---------------- MP4 writer ----------------


def _box(typ: bytes, *payloads: bytes) -> bytes:
    payload = b"".join(payloads)
    return struct.pack(">I4s", 8 + len(payload), typ) + payload


def _mvhd(duration_ms: int) -> bytes:
    return _box(b"mvhd",
                struct.pack(">B3s", 0, b"\x00\x00\x00"),
                struct.pack(">IIII", 0, 0, 1000, duration_ms),
                struct.pack(">IhH", 0x00010000, 0x0100, 0),
                struct.pack(">II", 0, 0),
                struct.pack(">9i", 0x10000, 0, 0, 0, 0x10000, 0, 0, 0,
                            0x40000000),
                struct.pack(">6I", 0, 0, 0, 0, 0, 0),
                struct.pack(">I", 3))


def _tkhd(track_id, duration_ms, width, height, volume) -> bytes:
    return _box(b"tkhd",
                struct.pack(">B3s", 0, b"\x00\x00\x07"),
                struct.pack(">II", 0, 0),
                struct.pack(">I", track_id),
                struct.pack(">I", 0),
                struct.pack(">I", duration_ms),
                struct.pack(">II", 0, 0),
                struct.pack(">hh", 0, 0),
                struct.pack(">hh", volume, 0),
                struct.pack(">9i", 0x10000, 0, 0, 0, 0x10000, 0, 0, 0,
                            0x40000000),
                struct.pack(">II", width << 16, height << 16))


def _mdhd(timescale, duration) -> bytes:
    return _box(b"mdhd",
                struct.pack(">B3s", 0, b"\x00\x00\x00"),
                struct.pack(">II", 0, 0),
                struct.pack(">I", timescale),
                struct.pack(">I", duration),
                struct.pack(">HH", 0x55C4, 0))


def _hdlr(handler: bytes, name: str) -> bytes:
    return _box(b"hdlr",
                struct.pack(">B3s", 0, b"\x00\x00\x00"),
                struct.pack(">I", 0), handler,
                struct.pack(">III", 0, 0, 0),
                name.encode("ascii") + b"\x00")


def _video_entry(fmt: bytes, width: int, height: int) -> bytes:
    body = (b"\x00" * 6 + struct.pack(">H", 1) + b"\x00" * 16
            + struct.pack(">HH", width, height)
            + struct.pack(">II", 0x00480000, 0x00480000)
            + struct.pack(">I", 0) + struct.pack(">H", 1)
            + b"\x00" * 32 + struct.pack(">Hh", 0x18, -1))
    return struct.pack(">I4s", 8 + len(body), fmt) + body


def _audio_entry(fmt: bytes, channels: int, sample_rate: int) -> bytes:
    body = (b"\x00" * 6 + struct.pack(">H", 1) + b"\x00" * 8
            + struct.pack(">HH", channels, 16)
            + struct.pack(">HH", 0, 0)
            + struct.pack(">I", sample_rate << 16))
    return struct.pack(">I4s", 8 + len(body), fmt) + body


def _stsd(entry: bytes) -> bytes:
    return _box(b"stsd", struct.pack(">B3s", 0, b"\x00\x00\x00"),
                struct.pack(">I", 1), entry)


def _stts(count: int, delta: int) -> bytes:
    return _box(b"stts", struct.pack(">B3s", 0, b"\x00\x00\x00"),
                struct.pack(">I", 1), struct.pack(">II", count, delta))


def _trak(track_id, handler, timescale, duration, tkhd_ms, width, height,
          stsd, stts, name) -> bytes:
    volume = 0x0100 if handler == b"soun" else 0
    mdia = _box(b"mdia", _mdhd(timescale, duration), _hdlr(handler, name),
                _box(b"minf", _box(b"stbl", stsd, stts)))
    return _box(b"trak", _tkhd(track_id, tkhd_ms, width, height, volume),
                mdia)


def write_mp4(video_duration_ms=5000, audio_duration_ms=5000, width=1920,
              height=1080, video_fmt=b"avc1", audio_fmt=b"mp4a",
              sample_rate=48000, channels=2, video_timescale=24000,
              audio_timescale=48000, fps_delta=1000,
              include_video=True, include_audio=True) -> bytes:
    vdur = video_duration_ms * video_timescale // 1000
    adur = audio_duration_ms * audio_timescale // 1000
    traks = []
    if include_video:
        traks.append(_trak(1, b"vide", video_timescale, vdur,
                           video_duration_ms, width, height,
                           _stsd(_video_entry(video_fmt, width, height)),
                           _stts(vdur // fps_delta if fps_delta else 0,
                                 fps_delta), "VideoHandler"))
    if include_audio:
        traks.append(_trak(2, b"soun", audio_timescale, adur,
                           audio_duration_ms, 0, 0,
                           _stsd(_audio_entry(audio_fmt, channels,
                                              sample_rate)),
                           _stts(adur // 1024, 1024), "SoundHandler"))
    ftyp = _box(b"ftyp", b"isom", struct.pack(">I", 0x200),
                b"isom", b"iso2", b"avc1", b"mp41")
    moov = _box(b"moov",
                _mvhd(max(video_duration_ms, audio_duration_ms)), *traks)
    return ftyp + moov + _box(b"mdat")


# ---------------- Matroska writer ----------------


def _eid(value: int) -> bytes:
    return value.to_bytes(max(1, (value.bit_length() + 7) // 8), "big")


def _esize(value: int) -> bytes:
    length = 1
    while value >= (1 << (7 * length)) - 1:
        length += 1
    return ((1 << (7 * length)) | value).to_bytes(length, "big")


def _elem(eid: int, payload: bytes) -> bytes:
    return _eid(eid) + _esize(len(payload)) + payload


def _uint(eid: int, value: int) -> bytes:
    return _elem(eid, value.to_bytes(max(1, (value.bit_length() + 7) // 8),
                                     "big"))


def _float(eid: int, value: float) -> bytes:
    return _elem(eid, struct.pack(">d", value))


def _str(eid: int, text: str) -> bytes:
    return _elem(eid, text.encode("ascii"))


def write_mkv(duration_ms=5000.0, width=1920, height=1080,
              sample_rate=48000.0, channels=2,
              video_codec="V_MPEG4/ISO/AVC", audio_codec="A_AAC",
              default_duration=41666666, track_numbers=(1, 2)) -> bytes:
    ebml = _elem(0x1A45DFA3,
                 _uint(0x4286, 1) + _uint(0x42F7, 1) + _uint(0x42F2, 4)
                 + _uint(0x42F3, 8) + _str(0x4282, "matroska")
                 + _uint(0x4287, 4) + _uint(0x4285, 2))
    info = _elem(0x1549A966,
                 _uint(0x2AD7B1, 1000000) + _float(0x4489, duration_ms))
    video = _elem(0xE0, _uint(0xB0, width) + _uint(0xBA, height))
    audio = _elem(0xE1, _float(0xB5, sample_rate) + _uint(0x9F, channels))
    t1 = _elem(0xAE, _uint(0xD7, track_numbers[0]) + _uint(0x73C5, 101)
               + _uint(0x83, 1) + _str(0x86, video_codec)
               + _uint(0x23E383, default_duration) + video)
    t2 = _elem(0xAE, _uint(0xD7, track_numbers[1]) + _uint(0x73C5, 102)
               + _uint(0x83, 2) + _str(0x86, audio_codec) + audio)
    tracks = _elem(0x1654AE6B, t1 + t2)
    segment = _eid(0x18538067) + b"\xff" + info + tracks  # unknown size
    return ebml + segment


# ---------------- sample set ----------------


def build_set(out_dir, inject_drift: bool = False) -> dict:
    out = Path(out_dir)
    out.mkdir(parents=True, exist_ok=True)
    payloads = {
        "master.mp4": write_mp4(),
        "preview.mp4": write_mp4(
            video_duration_ms=5017 if inject_drift else 5000),
        "diagnostic.mkv": write_mkv(
            sample_rate=44100.0 if inject_drift else 48000.0),
        "review.mkv": write_mkv(),
    }
    for name in FILES:
        (out / name).write_bytes(payloads[name])
    manifest = {
        "show_id": SHOW_ID,
        "tolerances": {"duration_ms": 10, "frame_rate": 0.01},
        "tracks": [
            {"role": "video", "type": "video", "required": True},
            {"role": "audio", "type": "audio", "required": True},
        ],
        "expect": {
            "video": {"codec": "h264", "width": 1920, "height": 1080,
                      "frame_rate": 24.0},
            "audio": {"codec": "aac", "sample_rate": 48000, "channels": 2},
        },
        "files": FILES,
    }
    (out / "manifest.json").write_text(
        json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    return manifest


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", required=True, help="output directory")
    parser.add_argument("--inject-drift", action="store_true",
                        help="introduce duration and codec parameter drift")
    args = parser.parse_args(argv)
    manifest = build_set(args.out, inject_drift=args.inject_drift)
    mode = "drift" if args.inject_drift else "clean"
    print(f"wrote {len(manifest['files'])} files ({mode}) to {args.out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
