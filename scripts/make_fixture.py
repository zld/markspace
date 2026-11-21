#!/usr/bin/env python3.11
"""Generate minimal Matroska fixtures for the mkvmp4 test suite.

Everything is synthesised from code: no external media files are referenced.
The payload bytes are deliberately compressed-looking filler, not real
elementary streams, because the pipeline only ever copies payload verbatim.

Usage:
    python scripts/make_fixture.py --out /tmp/sample.mkv
    python scripts/make_fixture.py --kind unknown-cluster --out /tmp/edge.mkv
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

# ---------------------------------------------------------------------------
# Tiny EBML writer (independent of the package under test so a fixture bug and
# a parser bug cannot cancel out).
# ---------------------------------------------------------------------------

ID_EBML = 0x1A45DFA3
ID_DOCTYPE = 0x4282
ID_EBML_VERSION = 0x4286
ID_SEGMENT = 0x18538067
ID_INFO = 0x1549A966
ID_TIMESTAMP_SCALE = 0x2AD7B1
ID_MUXING_APP = 0x4D80
ID_WRITING_APP = 0x5741
ID_TRACKS = 0x1654AE6B
ID_TRACK_ENTRY = 0xAE
ID_TRACK_NUMBER = 0xD7
ID_TRACK_UID = 0x73C5
ID_TRACK_TYPE = 0x83
ID_CODEC_ID = 0x86
ID_CODEC_PRIVATE = 0x63A2
ID_DEFAULT_DURATION = 0x23E383
ID_LANGUAGE = 0x22B59C
ID_VIDEO = 0xE0
ID_PIXEL_WIDTH = 0xB0
ID_PIXEL_HEIGHT = 0xBA
ID_AUDIO = 0xE1
ID_SAMPLING_FREQUENCY = 0xB5
ID_CHANNELS = 0x9F
ID_CLUSTER = 0x1F43B675
ID_CLUSTER_TIMESTAMP = 0xE7
ID_SIMPLE_BLOCK = 0xA3

UNKNOWN_SIZE = b"\xff"

AVC_PRIVATE = bytes(
    [
        0x01, 0x64, 0x00, 0x1F,  # configurationVersion, profile, compat, level
        0xFF, 0xE1, 0x00, 0x04,  # lengthSizeMinusOne, SPS count, SPS length
        0x67, 0x64, 0x00, 0x1F,  # SPS bytes
        0x01, 0x00, 0x04, 0x68, 0xEE, 0x3C, 0x80,  # PPS count + PPS
    ]
)
AAC_PRIVATE = bytes([0x12, 0x10])  # AAC-LC, 48 kHz, stereo


def encode_id(element_id: int) -> bytes:
    width = (element_id.bit_length() + 7) // 8
    return element_id.to_bytes(width, "big")


def encode_size(size: int | None) -> bytes:
    """Smallest VINT size; ``None`` produces the unknown-size marker."""
    if size is None:
        return UNKNOWN_SIZE
    for width in range(1, 9):
        if size < (1 << (7 * width)) - 1:
            return ((1 << (7 * width)) | size).to_bytes(width, "big")
    raise ValueError(f"size {size} too large for a VINT")


def element(element_id: int, payload: bytes, *, unknown_size: bool = False) -> bytes:
    return (
        encode_id(element_id)
        + encode_size(None if unknown_size else len(payload))
        + payload
    )


def uint_element(element_id: int, value: int, width: int | None = None) -> bytes:
    if width is None:
        width = max(1, (value.bit_length() + 7) // 8)
    return element(element_id, value.to_bytes(width, "big"))


def string_element(element_id: int, value: str) -> bytes:
    return element(element_id, value.encode("utf-8"))


def float_element(element_id: int, value: float) -> bytes:
    import struct

    return element(element_id, struct.pack(">f", value))


def block_header(track: int, relative_ms: int, flags: int) -> bytes:
    """Track number as an EBML VINT (marker retained), then timecode+flags."""
    if not 0 < track < 0x7F:
        raise ValueError("fixture track numbers stay in one VINT byte")
    return (
        bytes([0x80 | track])
        + int(relative_ms).to_bytes(2, "big", signed=True)
        + bytes([flags])
    )


def simple_block(track: int, relative_ms: int, flags: int, payload: bytes) -> bytes:
    return element(ID_SIMPLE_BLOCK, block_header(track, relative_ms, flags) + payload)


def payload(tag: str, size: int) -> bytes:
    """Deterministic pseudo-random filler with a recognisable prefix."""
    raw = (tag.encode() * ((size // max(1, len(tag))) + 1))[:size]
    return raw


# ---------------------------------------------------------------------------
# Fixture documents
# ---------------------------------------------------------------------------


def build_sample_document(
    *, unknown_segment: bool = False, audio_codec: str = "A_AAC"
) -> bytes:
    """Two tracks (H.264 + AAC), four clusters with the edge cases we test.

    Timestamps (cluster ms + block relative ms):
      video: 0, 100, 170 (gap), 166 (backwards -> repaired)
      audio: laced triples at 0 and 100, spaced by DefaultDuration
    The 250 ms offset applied by the demo pushes nothing negative.
    """
    header = element(
        ID_EBML,
        uint_element(ID_EBML_VERSION, 1)
        + string_element(ID_DOCTYPE, "matroska"),
    )
    info = element(
        ID_INFO,
        uint_element(ID_TIMESTAMP_SCALE, 1_000_000, 3)
        + string_element(ID_MUXING_APP, "mkvmp4 fixture generator")
        + string_element(ID_WRITING_APP, "scripts/make_fixture.py"),
    )
    video = element(
        ID_TRACK_ENTRY,
        uint_element(ID_TRACK_NUMBER, 1)
        + uint_element(ID_TRACK_UID, 0xA1)
        + uint_element(ID_TRACK_TYPE, 1)
        + string_element(ID_CODEC_ID, "V_MPEG4/ISO/AVC")
        + element(ID_CODEC_PRIVATE, AVC_PRIVATE)
        + uint_element(ID_DEFAULT_DURATION, 33_333_333)
        + element(
            ID_VIDEO,
            uint_element(ID_PIXEL_WIDTH, 320) + uint_element(ID_PIXEL_HEIGHT, 240),
        ),
    )
    audio = element(
        ID_TRACK_ENTRY,
        uint_element(ID_TRACK_NUMBER, 2)
        + uint_element(ID_TRACK_UID, 0xA2)
        + uint_element(ID_TRACK_TYPE, 2)
        + string_element(ID_CODEC_ID, audio_codec)
        + element(ID_CODEC_PRIVATE, AAC_PRIVATE)
        + uint_element(ID_DEFAULT_DURATION, 21_333_333)
        + string_element(ID_LANGUAGE, "eng")
        + element(
            ID_AUDIO,
            float_element(ID_SAMPLING_FREQUENCY, 48_000.0)
            + uint_element(ID_CHANNELS, 2),
        ),
    )
    tracks = element(ID_TRACKS, video + audio)

    # Laced Xiph audio block: 2 frames of 12 and 15 bytes plus a 20-byte tail.
    lace = bytes([2, 12, 15]) + payload("AUD1", 12) + payload("AUD2", 15) + payload(
        "AUD3", 20
    )
    clusters = b"".join(
        [
            element(
                ID_CLUSTER,
                uint_element(ID_CLUSTER_TIMESTAMP, 0)
                + simple_block(1, 0, 0x80, payload("VID1", 40))
                + simple_block(2, 0, 0x82, lace),
            ),
            # Unknown-size cluster: ends where the next cluster starts.
            element(
                ID_CLUSTER,
                uint_element(ID_CLUSTER_TIMESTAMP, 100)
                + simple_block(1, 0, 0x00, payload("VID2", 35))
                + simple_block(2, 0, 0x82, bytes([2, 10, 10]) + payload("AUD4", 10)
                               + payload("AUD5", 10) + payload("AUD6", 22)),
                unknown_size=True,
            ),
            element(
                ID_CLUSTER,
                uint_element(ID_CLUSTER_TIMESTAMP, 170)
                + simple_block(1, 0, 0x80, payload("VID3", 30)),
            ),
            element(
                ID_CLUSTER,
                uint_element(ID_CLUSTER_TIMESTAMP, 200)
                # -34 ms relative: an out-of-order sample inside a later cluster.
                + simple_block(1, -34, 0x00, payload("VID4", 33)),
            ),
        ]
    )
    segment_payload = info + tracks + clusters
    return header + element(ID_SEGMENT, segment_payload, unknown_size=unknown_segment)


def build_truncated_document() -> bytes:
    doc = build_sample_document()
    return doc[: len(doc) - 12]


def build_bad_vint_document() -> bytes:
    doc = build_sample_document()
    # Corrupt the Segment size field into a zero byte (illegal VINT).
    segment_offset = doc.index(b"\x18\x53\x80\x67") + 4
    return doc[:segment_offset] + b"\x00" + doc[segment_offset + 1 :]


def build_unsupported_codec_document() -> bytes:
    """A FLAC track: structurally valid, deliberately outside the subset."""
    return build_sample_document(audio_codec="A_FLAC")


FIXTURES = {
    "sample": build_sample_document,
    "unknown-segment": lambda: build_sample_document(unknown_segment=True),
    "truncated": build_truncated_document,
    "bad-vint": build_bad_vint_document,
    "unsupported-codec": build_unsupported_codec_document,
}


def write_fixture(kind: str, out: Path) -> int:
    out.parent.mkdir(parents=True, exist_ok=True)
    data = FIXTURES[kind]()
    out.write_bytes(data)
    return len(data)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", required=True, help="path of the .mkv to write")
    parser.add_argument(
        "--kind",
        choices=sorted(FIXTURES),
        default="sample",
        help="fixture variant (default: sample)",
    )
    args = parser.parse_args(argv)
    size = write_fixture(args.kind, Path(args.out))
    print(f"wrote {args.kind} fixture: {args.out} ({size} bytes)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
