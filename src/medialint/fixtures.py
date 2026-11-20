"""Fixture generator: synthetic MP4/WAV media and delivery manifests.

Builds a clean sample bundle and can inject delivery faults:
  duration   - one audio track runs longer than the manifest allows
  aac-rate   - one AAC track carries the wrong sample rate
  truncated  - the WAV asset is cut off mid-chunk
"""
from __future__ import annotations

import json
import struct
from pathlib import Path

FAULTS = ("duration", "aac-rate", "truncated")

DURATION_MS = 4000
FAULT_DURATION_MS = 4300          # 300ms over, beyond any sane tolerance
VIDEO_SIZE = (1920, 1080)
AUDIO_RATE = 48000
FAULT_AAC_RATE = 44100
AUDIO_CHANNELS = 2
LANG2_TO_3 = {"en": "eng", "zh": "zho", "ja": "jpn", "ko": "kor"}
RATE_TO_INDEX = {96000: 0, 88200: 1, 64000: 2, 48000: 3, 44100: 4, 32000: 5,
                 24000: 6, 22050: 7, 16000: 8, 12000: 9, 11025: 10, 8000: 11}

_MATRIX = struct.pack(">9I", 0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000)


def _box(typ: bytes, payload: bytes) -> bytes:
    return struct.pack(">I", 8 + len(payload)) + typ + payload


def _box64(typ: bytes, payload: bytes) -> bytes:
    return struct.pack(">I4sQ", 1, typ, 16 + len(payload)) + payload


def _fullbox(typ: bytes, version: int, flags: int, payload: bytes) -> bytes:
    return _box(typ, struct.pack(">I", (version << 24) | flags) + payload)


def _mvhd(timescale: int, duration: int, next_track_id: int) -> bytes:
    body = struct.pack(">IIII", 0, 0, timescale, duration)
    body += struct.pack(">IHH", 0x10000, 0x100, 0) + b"\0" * 8
    body += _MATRIX + b"\0" * 24 + struct.pack(">I", next_track_id)
    return _fullbox(b"mvhd", 0, 0, body)


def _tkhd(track_id: int, duration_ms: int, width: int = 0, height: int = 0,
          volume: int = 0) -> bytes:
    body = struct.pack(">IIIII", 0, 0, track_id, 0, duration_ms) + b"\0" * 8
    body += struct.pack(">hhhh", 0, 0, volume, 0)
    body += _MATRIX + struct.pack(">II", width << 16, height << 16)
    return _fullbox(b"tkhd", 0, 0x7, body)


def _mdhd(timescale: int, duration: int, lang3: str) -> bytes:
    packed = 0
    for ch in lang3:
        packed = (packed << 5) | (ord(ch) - 0x60)
    body = struct.pack(">IIIIHH", 0, 0, timescale, duration, packed, 0)
    return _fullbox(b"mdhd", 0, 0, body)


def _hdlr(handler: bytes, name: bytes) -> bytes:
    return _fullbox(b"hdlr", 0, 0, b"\0" * 4 + handler + b"\0" * 12 + name + b"\0")


def _dinf() -> bytes:
    url = _fullbox(b"url ", 0, 1, b"")
    return _box(b"dinf", _fullbox(b"dref", 0, 0, struct.pack(">I", 1) + url))


def _stbl(sample_entry: bytes) -> bytes:
    stsd = _fullbox(b"stsd", 0, 0, struct.pack(">I", 1) + sample_entry)
    stts = _fullbox(b"stts", 0, 0, struct.pack(">III", 1, 1, 1000))
    stsc = _fullbox(b"stsc", 0, 0, struct.pack(">IIII", 1, 1, 1, 1))
    stsz = _fullbox(b"stsz", 0, 0, struct.pack(">III", 0, 1, 0))
    stco = _fullbox(b"stco", 0, 0, struct.pack(">II", 1, 0))
    return _box(b"stbl", stsd + stts + stsc + stsz + stco)


def _avc1_entry(width: int, height: int) -> bytes:
    body = b"\0" * 6 + struct.pack(">H", 1) + b"\0" * 16
    body += struct.pack(">HH", width, height)
    body += struct.pack(">II", 0x480000, 0x480000) + b"\0" * 4
    body += struct.pack(">H", 1) + b"\0" * 32 + struct.pack(">Hh", 0x18, -1)
    sps = b"\x67\x64\x00\x28\xac\xd9"
    pps = b"\x68\xeb\xec"
    avcc = bytes([1, 100, 0, 40, 0xFF, 0xE1]) + struct.pack(">H", len(sps)) + sps
    avcc += b"\x01" + struct.pack(">H", len(pps)) + pps
    return _box(b"avc1", body + _box(b"avcC", avcc))


def _descriptor(tag: int, payload: bytes) -> bytes:
    return bytes([tag, len(payload)]) + payload


def _esds(rate: int, channels: int) -> bytes:
    asc = ((2 << 11) | (RATE_TO_INDEX[rate] << 7) | (channels << 3)).to_bytes(2, "big")
    dec_config = bytes([0x40, 0x15]) + b"\0" * 3 + struct.pack(">II", 128000, 128000)
    es = struct.pack(">HB", 1, 0)
    es += _descriptor(0x04, dec_config + _descriptor(0x05, asc))
    es += _descriptor(0x06, b"\x02")
    return _fullbox(b"esds", 0, 0, _descriptor(0x03, es))


def _mp4a_entry(rate: int, channels: int) -> bytes:
    body = b"\0" * 6 + struct.pack(">H", 1) + b"\0" * 8
    body += struct.pack(">HHHHI", channels, 16, 0, 0, rate << 16)
    return _box(b"mp4a", body + _esds(rate, channels))


def _video_trak(track_id: int, duration_ms: int) -> bytes:
    mdia = _mdhd(90000, duration_ms * 90, "und") + _hdlr(b"vide", b"VideoHandler")
    minf = _fullbox(b"vmhd", 0, 1, b"\0" * 8) + _dinf() + _stbl(_avc1_entry(*VIDEO_SIZE))
    return _box(b"trak", _tkhd(track_id, duration_ms, *VIDEO_SIZE) + _box(b"mdia", mdia + _box(b"minf", minf)))


def _audio_trak(track_id: int, lang3: str, rate: int, duration_ms: int) -> bytes:
    mdia = _mdhd(rate, rate * duration_ms // 1000, lang3) + _hdlr(b"soun", b"SoundHandler")
    minf = _fullbox(b"smhd", 0, 0, b"\0" * 4) + _dinf() + _stbl(_mp4a_entry(rate, AUDIO_CHANNELS))
    return _box(b"trak", _tkhd(track_id, duration_ms, volume=0x100) + _box(b"mdia", mdia + _box(b"minf", minf)))


def build_intro_mp4(faults: set[str]) -> bytes:
    en_ms = FAULT_DURATION_MS if "duration" in faults else DURATION_MS
    zh_rate = FAULT_AAC_RATE if "aac-rate" in faults else AUDIO_RATE
    ftyp = _box(b"ftyp", b"isom" + struct.pack(">I", 512) + b"isomavc1mp41")
    free = _box64(b"free", b"\0" * 24)          # exercises 64-bit box sizes
    skip = _box(b"skip", b"unknown box payload")  # exercises unknown-box skipping
    mdat = _box(b"mdat", b"\0" * 256)
    moov = _mvhd(1000, DURATION_MS, 4)
    moov += _video_trak(1, DURATION_MS)
    moov += _audio_trak(2, LANG2_TO_3["en"], AUDIO_RATE, en_ms)
    moov += _audio_trak(3, LANG2_TO_3["zh"], zh_rate, DURATION_MS)
    moov += _box(b"udta", b"\xa9too" + b"\0" * 8)
    return ftyp + free + skip + mdat + _box(b"moov", moov)  # moov at file end


def build_voiceover_wav(faults: set[str]) -> bytes:
    block = AUDIO_CHANNELS * 2
    samples = AUDIO_RATE * DURATION_MS // 1000
    fmt = struct.pack("<HHIIHH", 1, AUDIO_CHANNELS, AUDIO_RATE,
                      AUDIO_RATE * block, block, 16)
    payload = b"WAVE" + _riff_chunk(b"fmt ", fmt) + _riff_chunk(b"data", b"\0" * (samples * block))
    riff = b"RIFF" + struct.pack("<I", len(payload)) + payload
    if "truncated" in faults:
        riff = riff[: len(riff) * 3 // 5]
    return riff


def _riff_chunk(cid: bytes, payload: bytes) -> bytes:
    pad = b"\0" if len(payload) & 1 else b""
    return cid + struct.pack("<I", len(payload)) + payload + pad


def manifest_doc() -> dict:
    def audio(lang: str | None) -> dict:
        t = {"type": "audio", "codec": "aac" if lang else "pcm",
             "sample_rate": AUDIO_RATE, "channels": AUDIO_CHANNELS,
             "duration_ms": DURATION_MS}
        if lang:
            t["language"] = lang
        return t

    return {
        "bundle": "sample-bundle",
        "assets": [
            {"path": "media/intro.mp4", "tracks": [
                {"type": "video", "codec": "avc1", "width": VIDEO_SIZE[0],
                 "height": VIDEO_SIZE[1], "duration_ms": DURATION_MS},
                audio("en"), audio("zh"),
            ]},
            {"path": "media/voiceover.wav", "tracks": [audio(None)]},
        ],
    }


def write_bundle(out_dir: Path, inject: list[str]) -> list[Path]:
    unknown = set(inject) - set(FAULTS)
    if unknown:
        raise ValueError(f"unknown fault(s): {', '.join(sorted(unknown))} (choose from {', '.join(FAULTS)})")
    faults = set(inject)
    out_dir.mkdir(parents=True, exist_ok=True)
    written = []
    for rel, data in (("media/intro.mp4", build_intro_mp4(faults)),
                      ("media/voiceover.wav", build_voiceover_wav(faults))):
        p = out_dir / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_bytes(data)
        written.append(p)
    manifest = out_dir / "bundle.json"
    manifest.write_text(json.dumps(manifest_doc(), indent=2) + "\n", encoding="utf-8")
    written.append(manifest)
    return written
