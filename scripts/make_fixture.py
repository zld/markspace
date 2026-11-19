"""Generate synthetic Matroska fixtures without any external media.

The generator deliberately exercises the awkward parts of the format:
unknown-length clusters, non-zero cluster origins, signed relative block
timecodes, negative composition offsets and truncated inputs.
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

# Allow running as a bare script (`python scripts/make_fixture.py`) against
# the src/ layout without requiring an install or PYTHONPATH.
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from meshift.ebml import (BLOCK, BLOCK_GROUP, CLUSTER, CODEC_ID, COMPOSITION_OFFSET,
                          DEFAULT_DURATION, EBML_HEADER, INFO, REFERENCE_BLOCK,
                          SEGMENT, SIMPLE_BLOCK, TIMECODE, TIMECODE_SCALE,
                          TRACKS, TRACK_ENTRY, TRACK_NUMBER, TRACK_TYPE)


def encode_vint(value: int, width: int | None = None) -> bytes:
    if width is None:
        width = max(1, (value.bit_length() + 6) // 7)
    marker = 1 << (7 * width)
    raw = value + marker
    return raw.to_bytes(width, "big")


def encode_element_id(element_id: int) -> bytes:
    """EBML ids are raw class identifiers with a built-in length marker."""
    width = max(1, (element_id.bit_length() + 7) // 8)
    return element_id.to_bytes(width, "big")


def encode_unknown_vint(width: int = 8) -> bytes:
    return bytes([0x01] + [0xFF] * (width - 1))


def element(element_id: int, payload: bytes, *, unknown_length: bool = False
            ) -> bytes:
    id_bytes = encode_element_id(element_id)
    if unknown_length:
        return id_bytes + encode_unknown_vint() + payload
    return id_bytes + encode_vint(len(payload)) + payload


def uint_element(element_id: int, value: int) -> bytes:
    length = max(1, (value.bit_length() + 7) // 8)
    return element(element_id, value.to_bytes(length, "big"))


def text_element(element_id: int, value: str) -> bytes:
    return element(element_id, value.encode("utf-8"))


def block_bytes(track_number: int, relative_timecode: int, flags: int,
                payload: bytes) -> bytes:
    return (encode_vint(track_number)
            + relative_timecode.to_bytes(2, "big", signed=True)
            + bytes([flags]) + payload)


def make_ebml_header() -> bytes:
    return element(EBML_HEADER, b"")


def make_info(timecode_scale: int = 1_000_000) -> bytes:
    return element(INFO, uint_element(TIMECODE_SCALE, timecode_scale))


def make_tracks() -> bytes:
    video = element(TRACK_ENTRY,
                    uint_element(TRACK_NUMBER, 1)
                    + uint_element(TRACK_TYPE, 1)
                    + text_element(CODEC_ID, "V_VP9")
                    + uint_element(DEFAULT_DURATION, 100_000_000))
    audio = element(TRACK_ENTRY,
                    uint_element(TRACK_NUMBER, 2)
                    + uint_element(TRACK_TYPE, 2)
                    + text_element(CODEC_ID, "A_OPUS"))
    return element(TRACKS, video + audio)


def make_simple_block(track: int, relative: int, keyframe: bool,
                      marker: int) -> bytes:
    flags = 0x80 if keyframe else 0x00
    payload = bytes([0xB0 + marker, 0x01]) * (4 + marker)
    return element(SIMPLE_BLOCK, block_bytes(track, relative, flags, payload))


def make_block_group(track: int, relative: int, marker: int,
                     composition_offset: int) -> bytes:
    payload = bytes([0xC0 + marker, 0x02]) * 4
    block = element(BLOCK, block_bytes(track, relative, 0x00, payload))
    # ReferenceBlock present => non-keyframe; signed private composition id.
    ref = element(REFERENCE_BLOCK, (1).to_bytes(1, "big", signed=True))
    offset = element(COMPOSITION_OFFSET,
                     composition_offset.to_bytes(2, "big", signed=True))
    return element(BLOCK_GROUP, ref + block + offset)


def make_cluster(base_timecode: int, body: bytes,
                 unknown_length: bool) -> bytes:
    header = uint_element(TIMECODE, base_timecode)
    return element(CLUSTER, header + body, unknown_length=unknown_length)


def build_main_fixture() -> bytes:
    # Cluster A: known length, starts at 50 ms, four video frames.
    body_a = b"".join([
        make_simple_block(1, 0, True, 0),
        make_simple_block(1, 100, False, 1),
        make_simple_block(2, 0, True, 2),
        make_simple_block(1, 200, False, 3),
    ])
    cluster_a = make_cluster(50, body_a, unknown_length=False)

    # Cluster B: unknown length, origin 350 ms, negative composition offset.
    body_b = b"".join([
        make_simple_block(1, 0, True, 4),
        make_block_group(1, 100, 5, composition_offset=-50),
        make_simple_block(2, 0, True, 6),
        make_simple_block(1, 200, False, 7),
    ])
    cluster_b = make_cluster(350, body_b, unknown_length=True)

    # Cluster C: known length following an unknown-length sibling, proving
    # the parser closes B at the next sibling rather than the buffer end.
    body_c = b"".join([
        make_simple_block(1, 0, True, 8),
        make_simple_block(2, 0, True, 9),
        make_simple_block(1, 100, False, 10),
        make_simple_block(1, 200, False, 11),
    ])
    cluster_c = make_cluster(650, body_c, unknown_length=False)

    segment = element(SEGMENT, make_info() + make_tracks()
                      + cluster_a + cluster_b + cluster_c)
    return make_ebml_header() + segment


def write_fixture(path: Path) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(build_main_fixture())


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Build meshift MKV fixture")
    parser.add_argument("--out", required=True, type=Path)
    parser.add_argument("--truncated", type=Path,
                        help="also write a truncated copy to this path")
    args = parser.parse_args(argv)
    write_fixture(args.out)
    if args.truncated:
        raw = build_main_fixture()
        args.truncated.write_bytes(raw[: len(raw) - 7])
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
