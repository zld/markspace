"""Deterministic multi-track Matroska fixture generator.

Builds a small but realistic MKV entirely in code (no external samples):
AVC video + AAC audio + an unsupported subtitle track, unknown-size
Segment and one unknown-size Cluster, and a slightly negative audio
start to exercise timestamp shifting.
"""
from __future__ import annotations

import struct

from . import matroska as M
from .elements import ID_EBML_HEADER, ID_SEGMENT, ID_CLUSTER, ID_SIMPLE_BLOCK

_UNKNOWN_SIZE_1 = b"\xff"                            # 1-byte all-ones
_UNKNOWN_SIZE_8 = b"\x01" + b"\xff" * 7              # 8-byte all-ones

# Minimal but well-formed AVC decoder config (SPS/PPS for 640x360 baseline).
_SPS = bytes.fromhex("6764001facd9405005bb011000000300100000030320f1831960")
_PPS = bytes.fromhex("68ebe3cb22c0")
AVCC = (b"\x01\x64\x00\x1f" + b"\xfc" + b"\xe1"
        + struct.pack(">H", len(_SPS)) + _SPS
        + b"\x01" + struct.pack(">H", len(_PPS)) + _PPS)
AAC_ASC = bytes.fromhex("1210")  # AAC-LC, 44100 Hz, stereo

VIDEO_FPS = 30
VIDEO_FRAMES = 90          # 3 seconds
AUDIO_RATE = 44100
AUDIO_FRAME_SAMPLES = 1024
CLUSTER_FRAMES = 30        # one video second per cluster


def _eid(eid: int) -> bytes:
    return eid.to_bytes((eid.bit_length() + 7) // 8, "big")


def _size(value: int) -> bytes:
    width = 1
    while value >= (1 << (7 * width)) - 1:
        width += 1
    return (value | (1 << (7 * width))).to_bytes(width, "big")


def _el(eid: int, payload: bytes) -> bytes:
    return _eid(eid) + _size(len(payload)) + payload


def _uint(eid: int, value: int) -> bytes:
    return _el(eid, value.to_bytes(max(1, (value.bit_length() + 7) // 8), "big"))


def _ebml_header() -> bytes:
    payload = b"".join([
        _uint(0x4286, 1), _uint(0x42F7, 1), _uint(0x42F2, 4), _uint(0x42F3, 8),
        _el(0x4282, b"matroska"), _uint(0x4287, 4), _uint(0x4285, 2),
    ])
    return _el(ID_EBML_HEADER, payload)


def _info() -> bytes:
    payload = b"".join([
        _uint(M.ID_TIMESTAMP_SCALE, M.DEFAULT_TIMESTAMP_SCALE),
        _el(0x4D80, b"mkvmp4-fixture"),
        _el(0x5741, b"mkvmp4"),
        _el(M.ID_DURATION, struct.pack(">d", 3000.0)),
    ])
    return _el(M.ID_INFO, payload)


def _track_entry(number, uid, ttype, codec, private=b"", default=1,
                 language="und", default_duration=0, extra=b"") -> bytes:
    payload = b"".join([
        _uint(M.ID_TRACK_NUMBER, number), _uint(M.ID_TRACK_UID, uid),
        _uint(M.ID_TRACK_TYPE, ttype), _uint(M.ID_FLAG_ENABLED, 1),
        _uint(M.ID_FLAG_DEFAULT, default), _el(M.ID_CODEC_ID, codec.encode()),
        _el(M.ID_LANGUAGE, language.encode()),
    ])
    if private:
        payload += _el(M.ID_CODEC_PRIVATE, private)
    if default_duration:
        payload += _uint(M.ID_DEFAULT_DURATION, default_duration)
    return _el(M.ID_TRACK_ENTRY, payload + extra)


def _tracks() -> bytes:
    video = _track_entry(
        1, 0x1001, M.TRACK_TYPE_VIDEO, "V_MPEG4/ISO/AVC",
        private=AVCC, default_duration=1_000_000_000 // VIDEO_FPS,
        extra=_el(M.ID_VIDEO, _uint(M.ID_PIXEL_WIDTH, 640)
                  + _uint(M.ID_PIXEL_HEIGHT, 360)))
    audio = _track_entry(
        2, 0x1002, M.TRACK_TYPE_AUDIO, "A_AAC", private=AAC_ASC,
        language="eng", default_duration=AUDIO_FRAME_SAMPLES * 1_000_000_000 // AUDIO_RATE,
        extra=_el(M.ID_AUDIO, _el(M.ID_SAMPLING_FREQ,
                                  struct.pack(">d", float(AUDIO_RATE)))
                  + _uint(M.ID_CHANNELS, 2)))
    subtitle = _track_entry(3, 0x1003, M.TRACK_TYPE_SUBTITLE, "S_TEXT/UTF8",
                            default=0)
    return _el(M.ID_TRACKS, video + audio + subtitle)


def _video_sample(frame: int) -> bytes:
    key = frame % CLUSTER_FRAMES == 0
    nal = bytes([(0x65 if key else 0x41)])
    nal += bytes(((frame * 7 + j) & 0xFF) or 1 for j in range(60 + frame % 40))
    return struct.pack(">I", len(nal)) + nal  # AVCC length-prefixed


def _audio_sample(frame: int) -> bytes:
    return bytes(((frame * 13 + j * 5) & 0xFF) or 1 for j in range(24 + frame % 16))


def _simple_block(track: int, rel_ts: int, keyframe: bool, payload: bytes) -> bytes:
    flags = 0x80 if keyframe else 0x00
    head = _size(track) + struct.pack(">h", rel_ts) + bytes([flags])
    return _el(ID_SIMPLE_BLOCK, head + payload)


def _cluster(index_: int, unknown_size: bool) -> bytes:
    base_ms = index_ * 1000
    payload = _uint(M.ID_TIMESTAMP, base_ms)
    for f in range(index_ * CLUSTER_FRAMES, (index_ + 1) * CLUSTER_FRAMES):
        ts_ms = round(f * 1000 / VIDEO_FPS)
        payload += _simple_block(1, ts_ms - base_ms,
                                 f % CLUSTER_FRAMES == 0, _video_sample(f))
    first = index_ * AUDIO_RATE              # sample range of this second
    last = (index_ + 1) * AUDIO_RATE
    a_frame0 = first // AUDIO_FRAME_SAMPLES
    a_frame1 = -(-last // AUDIO_FRAME_SAMPLES)
    for a in range(a_frame0, a_frame1):
        ts_ms = round(a * AUDIO_FRAME_SAMPLES * 1000 / AUDIO_RATE)
        rel = ts_ms - base_ms
        if a == 0:
            rel = -2  # exercise negative-start handling
        payload += _simple_block(2, rel, True, _audio_sample(a))
    if index_ == 0:
        payload += _simple_block(3, 500 - base_ms, True, b"fixture subtitle")
    if unknown_size:
        return _eid(ID_CLUSTER) + _UNKNOWN_SIZE_1 + payload
    return _el(ID_CLUSTER, payload)


def build_fixture() -> bytes:
    """Return a complete multi-track Matroska file as bytes."""
    n_clusters = VIDEO_FRAMES // CLUSTER_FRAMES
    segment = _info() + _tracks()
    segment += b"".join(_cluster(i, unknown_size=(i == 1))
                        for i in range(n_clusters))
    return _ebml_header() + _eid(ID_SEGMENT) + _UNKNOWN_SIZE_8 + segment
