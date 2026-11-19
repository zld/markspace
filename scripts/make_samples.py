#!/usr/bin/env python3
"""Generate local sample media files for medcheck demos and tests.

Writes everything from scratch with struct — no external media, no
third-party tools. The default set is three files with four tracks in
total: two clean WAVs and one MOV whose audio track duration exceeds the
container duration by ~120 ms, producing a reviewable mismatch.
"""

from __future__ import annotations

import argparse
import os
import struct

# ---------------------------------------------------------------- WAV


def write_wav(path: str, rate: int, channels: int, bits: int,
              duration_ms: int) -> None:
    block_align = channels * bits // 8
    byte_rate = rate * block_align
    data_len = byte_rate * duration_ms // 1000
    data_len -= data_len % block_align
    fmt = struct.pack("<HHIIHH", 1, channels, rate, byte_rate, block_align,
                      bits)
    payload = (b"WAVE"
               + b"fmt " + struct.pack("<I", len(fmt)) + fmt
               + b"data" + struct.pack("<I", data_len) + b"\0" * data_len)
    with open(path, "wb") as fh:
        fh.write(b"RIFF" + struct.pack("<I", len(payload)) + payload)


# ---------------------------------------------------------------- MOV

def _box(btype: bytes, payload: bytes) -> bytes:
    return struct.pack(">I4s", 8 + len(payload), btype) + payload

_MATRIX = struct.pack(">9I", 0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0,
                      0x40000000)


def _mvhd(timescale: int, duration: int) -> bytes:
    p = (b"\0\0\0\0" + struct.pack(">IIII", 0, 0, timescale, duration)
         + struct.pack(">I", 0x00010000) + struct.pack(">H", 0x0100)
         + b"\0" * 10 + _MATRIX + b"\0" * 24 + struct.pack(">I", 3))
    return _box(b"mvhd", p)


def _tkhd(track_id: int, duration: int, width: int = 0, height: int = 0,
          volume: int = 0) -> bytes:
    p = (b"\0\0\0\3" + struct.pack(">IIIII", 0, 0, track_id, 0, duration)
         + b"\0" * 8 + struct.pack(">hhHh", 0, 0, volume, 0) + _MATRIX
         + struct.pack(">II", width << 16, height << 16))
    return _box(b"tkhd", p)


def _lang_bits(lang: str) -> int:
    bits = 0
    for ch in lang:
        bits = (bits << 5) | (ord(ch) - 0x60)
    return bits


def _mdhd(timescale: int, duration: int, lang: str = "eng") -> bytes:
    p = (b"\0\0\0\0" + struct.pack(">IIII", 0, 0, timescale, duration)
         + struct.pack(">HH", _lang_bits(lang), 0))
    return _box(b"mdhd", p)


def _hdlr(handler: bytes) -> bytes:
    return _box(b"hdlr", b"\0\0\0\0" + struct.pack(">I", 0) + handler
                + b"\0" * 12 + b"\0")


def _video_entry(codec: bytes, width: int, height: int) -> bytes:
    e = (b"\0" * 6 + struct.pack(">H", 1) + b"\0" * 16
         + struct.pack(">HH", width, height)
         + struct.pack(">II", 0x00480000, 0x00480000)
         + struct.pack(">I", 0) + struct.pack(">H", 1) + b"\0" * 32
         + struct.pack(">Hh", 0x18, -1))
    return struct.pack(">I4s", len(e) + 8, codec) + e


def _audio_entry(codec: bytes, channels: int, rate: int) -> bytes:
    e = (b"\0" * 6 + struct.pack(">H", 1) + b"\0" * 8
         + struct.pack(">HHHHI", channels, 16, 0, 0, rate << 16))
    return struct.pack(">I4s", len(e) + 8, codec) + e


def _stbl(entry: bytes) -> bytes:
    stsd = _box(b"stsd", b"\0\0\0\0" + struct.pack(">I", 1) + entry)
    return _box(b"stbl", stsd)


def _video_trak(track_id: int, movie_duration: int, media_timescale: int,
                media_duration: int, width: int, height: int,
                codec: bytes = b"avc1", lang: str = "eng") -> bytes:
    minf = _box(b"minf", _box(b"vmhd", b"\0\0\0\1" + b"\0" * 8)
                + _stbl(_video_entry(codec, width, height)))
    mdia = _box(b"mdia", _mdhd(media_timescale, media_duration, lang)
                + _hdlr(b"vide") + minf)
    return _box(b"trak", _tkhd(track_id, movie_duration, width, height)
                + mdia)


def _audio_trak(track_id: int, movie_duration: int, media_timescale: int,
                media_duration: int, channels: int, rate: int,
                codec: bytes = b"mp4a", lang: str = "eng") -> bytes:
    minf = _box(b"minf", _box(b"smhd", b"\0\0\0\0" + b"\0" * 4)
                + _stbl(_audio_entry(codec, channels, rate)))
    mdia = _box(b"mdia", _mdhd(media_timescale, media_duration, lang)
                + _hdlr(b"soun") + minf)
    return _box(b"trak", _tkhd(track_id, movie_duration, volume=0x0100)
                + mdia)


def write_mov(path: str, traks: list[bytes], movie_timescale: int = 1000,
              movie_duration: int = 2000) -> None:
    ftyp = _box(b"ftyp", b"qt  " + struct.pack(">I", 0) + b"qt  ")
    moov = _box(b"moov", _mvhd(movie_timescale, movie_duration)
                + b"".join(traks))
    with open(path, "wb") as fh:
        fh.write(ftyp + moov + _box(b"mdat", b""))


# ---------------------------------------------------------------- main

def make_sample_set(out_dir: str) -> list[str]:
    os.makedirs(out_dir, exist_ok=True)
    paths = []
    wav1 = os.path.join(out_dir, "clean_stereo.wav")
    write_wav(wav1, rate=48000, channels=2, bits=16, duration_ms=1000)
    paths.append(wav1)
    wav2 = os.path.join(out_dir, "clean_mono.wav")
    write_wav(wav2, rate=44100, channels=1, bits=16, duration_ms=1500)
    paths.append(wav2)
    mov = os.path.join(out_dir, "promo.mov")
    # Container says 2000 ms; the audio media runs 2120 ms -> 120 ms drift.
    write_mov(mov, traks=[
        _video_trak(1, movie_duration=2000, media_timescale=90000,
                    media_duration=180000, width=1920, height=1080),
        _audio_trak(2, movie_duration=2120, media_timescale=48000,
                    media_duration=101760, channels=2, rate=48000),
    ], movie_duration=2000)
    paths.append(mov)
    return paths


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", required=True, help="output directory")
    args = parser.parse_args()
    for path in make_sample_set(args.out):
        print(path)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
