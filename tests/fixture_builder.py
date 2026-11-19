"""Synthetic Matroska fixture generation.

All sample data is produced by code in this repository; no external media or
third-party muxer is involved. The generator deliberately exercises the hard
cases: an unknown-length Segment, several cluster boundaries, a non-zero
start timecode, a legal negative block-relative timecode, both SimpleBlock
and BlockGroup structures and a Cues index.
"""

from __future__ import annotations

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "src"))

from mkvmp4 import ebml  # noqa: E402
from mkvmp4.matroska import (  # noqa: E402
    ID_AUDIO,
    ID_BIT_DEPTH,
    ID_BLOCK,
    ID_BLOCK_GROUP,
    ID_CHANNELS,
    ID_CLUSTER,
    ID_CLUSTER_TIMECODE,
    ID_CODEC_ID,
    ID_CODEC_PRIVATE,
    ID_CUES,
    ID_CUE_CLUSTER_POSITION,
    ID_CUE_POINT,
    ID_CUE_TIME,
    ID_CUE_TRACK,
    ID_CUE_TRACK_POSITIONS,
    ID_DEFAULT_DURATION,
    ID_DURATION,
    ID_EBML_DOC_TYPE,
    ID_EBML_DOC_TYPE_READ_VERSION,
    ID_EBML_DOC_TYPE_VERSION,
    ID_EBML_HEADER,
    ID_FLAG_ENABLED,
    ID_PIXEL_HEIGHT,
    ID_PIXEL_WIDTH,
    ID_SAMPLING_FREQUENCY,
    ID_SEGMENT,
    ID_SEGMENT_INFO,
    ID_SIMPLE_BLOCK,
    ID_TIMECODE_SCALE,
    ID_TRACKS,
    ID_TRACK_ENTRY,
    ID_TRACK_NUMBER,
    ID_TRACK_TYPE,
    ID_VIDEO,
    ID_VOID,
)

# Minimal but structurally plausible codec configuration blobs.
AVC_CONFIG = bytes.fromhex(
    "0142c01effe100096742c01eab405005b910000001000468ce3c80"
)
AAC_CONFIG = bytes.fromhex("1190")  # AAC-LC, 48 kHz, stereo


def _uint(eid: int, value: int) -> bytes:
    return ebml.encode_uint_element(eid, value)


def _float(eid: int, value: float) -> bytes:
    import struct

    return ebml.encode_element(eid, struct.pack(">d", value))


def _string(eid: int, value: str) -> bytes:
    return ebml.encode_element(eid, value.encode("utf-8"))


def _binary(eid: int, value: bytes) -> bytes:
    return ebml.encode_element(eid, value)


def ebml_header() -> bytes:
    payload = b"".join([
        _uint(ID_EBML_DOC_TYPE_VERSION, 4),
        _uint(ID_EBML_DOC_TYPE_READ_VERSION, 2),
        _string(ID_EBML_DOC_TYPE, "matroska"),
    ])
    return ebml.encode_element(ID_EBML_HEADER, payload)


def video_track_entry() -> bytes:
    video = ebml.encode_element(ID_VIDEO, b"".join([
        _uint(ID_PIXEL_WIDTH, 320),
        _uint(ID_PIXEL_HEIGHT, 240),
    ]))
    payload = b"".join([
        _uint(ID_TRACK_NUMBER, 1),
        _uint(ID_TRACK_TYPE, 1),
        _string(ID_CODEC_ID, "V_MPEG4/ISO/AVC"),
        _uint(ID_FLAG_ENABLED, 1),
        _uint(ID_DEFAULT_DURATION, 40_000_000),   # 25 fps
        _binary(ID_CODEC_PRIVATE, AVC_CONFIG),
        video,
    ])
    return ebml.encode_element(ID_TRACK_ENTRY, payload)


def audio_track_entry() -> bytes:
    audio = ebml.encode_element(ID_AUDIO, b"".join([
        _float(ID_SAMPLING_FREQUENCY, 48000.0),
        _uint(ID_CHANNELS, 2),
        _uint(ID_BIT_DEPTH, 16),
    ]))
    payload = b"".join([
        _uint(ID_TRACK_NUMBER, 2),
        _uint(ID_TRACK_TYPE, 2),
        _string(ID_CODEC_ID, "A_AAC"),
        _uint(ID_FLAG_ENABLED, 1),
        _uint(ID_DEFAULT_DURATION, 21_333_333),  # 1024/48 kHz
        _binary(ID_CODEC_PRIVATE, AAC_CONFIG),
        audio,
    ])
    return ebml.encode_element(ID_TRACK_ENTRY, payload)


def tracks_element() -> bytes:
    return ebml.encode_element(
        ID_TRACKS, video_track_entry() + audio_track_entry())


def info_element() -> bytes:
    payload = b"".join([
        _uint(ID_TIMECODE_SCALE, 1_000_000),  # 1 ms units
        _float(ID_DURATION, 800.0),
    ])
    return ebml.encode_element(ID_SEGMENT_INFO, payload)


def simple_block(track_num: int, rel_ms: int, payload: bytes,
                 keyframe: bool) -> bytes:
    body = (
        ebml.encode_vint(track_num)
        + int(rel_ms).to_bytes(2, "big", signed=True)
        + bytes([0x80 if keyframe else 0x00])
        + ebml.encode_vint(len(payload))
        + payload
    )
    return ebml.encode_element(ID_SIMPLE_BLOCK, body)


def block_group_block(track_num: int, rel_ms: int, payload: bytes) -> bytes:
    body = (
        ebml.encode_vint(track_num)
        + int(rel_ms).to_bytes(2, "big", signed=True)
        + b"\x00"
        + ebml.encode_vint(len(payload))
        + payload
    )
    block = ebml.encode_element(ID_BLOCK, body)
    return ebml.encode_element(ID_BLOCK_GROUP, block)


def cluster(timecode_ms: int, blocks: list[bytes]) -> bytes:
    payload = _uint(ID_CLUSTER_TIMECODE, timecode_ms) + b"".join(blocks)
    return ebml.encode_element(ID_CLUSTER, payload)


def cues_element() -> bytes:
    positions = b"".join([
        _uint(ID_CUE_TRACK, 1),
        _uint(ID_CUE_CLUSTER_POSITION, 0),
    ])
    point = ebml.encode_element(ID_CUE_POINT, b"".join([
        _uint(ID_CUE_TIME, 500),
        ebml.encode_element(ID_CUE_TRACK_POSITIONS, positions),
    ]))
    return ebml.encode_element(ID_CUES, point)


def build_sample_mkv(path: str) -> str:
    """Write the canonical fixture and return its path.

    Layout: unknown-length Segment starting at a non-zero 500 ms origin;
    video track 1 has five frames (first carries a legal -10 ms relative
    timecode); audio track 2 has five AAC frames, one inside a BlockGroup.
    """
    video_frames = [bytes([0x65, 0x88, 0x01]) + bytes([i]) * 8
                    for i in range(5)]
    audio_frames = [bytes([0x21, 0x10]) + bytes([10 + i]) * 6
                    for i in range(5)]

    # Cluster 1 starts at 500 ms; the first video block sits at 490 ms.
    c1 = cluster(500, [
        simple_block(1, -10, video_frames[0], keyframe=True),
        simple_block(1, 40, video_frames[1], keyframe=False),
        simple_block(2, 0, audio_frames[0], keyframe=False),
    ])
    c2 = cluster(600, [
        simple_block(1, 0, video_frames[2], keyframe=True),
        block_group_block(2, 0, audio_frames[1]),
        simple_block(2, 21, audio_frames[2], keyframe=False),
    ])
    c3 = cluster(700, [
        simple_block(1, 0, video_frames[3], keyframe=False),
        simple_block(1, 40, video_frames[4], keyframe=True),
        simple_block(2, 0, audio_frames[3], keyframe=False),
        simple_block(2, 21, audio_frames[4], keyframe=False),
    ])

    segment_payload = b"".join([
        info_element(),
        tracks_element(),
        ebml.encode_element(ID_VOID, b"\x00" * 8),
        c1, c2, c3,
        cues_element(),
    ])
    # Unknown-length Segment: all-ones 8-octet VINT.
    segment = (
        ebml.encode_id(ID_SEGMENT)
        + ebml.unknown_size_vint(8)
        + segment_payload
    )
    data = ebml_header() + segment
    os.makedirs(os.path.dirname(os.path.abspath(path)), exist_ok=True)
    with open(path, "wb") as f:
        f.write(data)
    return path


# --------------------------------------------------------------------------
# Negative-case fixtures used by the test suite
# --------------------------------------------------------------------------

def write_raw(path: str, data: bytes) -> str:
    os.makedirs(os.path.dirname(os.path.abspath(path)), exist_ok=True)
    with open(path, "wb") as f:
        f.write(data)
    return path


def truncated_header_fixture(path: str) -> str:
    return write_raw(path, ebml_header()[:-3])


def bad_vint_fixture(path: str) -> str:
    # EBML header whose size VINT is a zero byte (illegal length marker).
    return write_raw(
        path, ebml.encode_id(ID_EBML_HEADER) + b"\x00\x00")
