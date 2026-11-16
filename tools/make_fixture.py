"""Generate synthetic Matroska fixtures entirely from code.

The generated file exercises every parser path the project promises to
handle:

* an unknown-size Segment and three unknown-size Clusters
  (``unknown_size_masters=3``);
* SimpleBlock blocks with a *negative* signed relative timecode and B-frame
  decode ordering (PTS out of order relative to DTS);
* an audio gap between clusters;
* Xiph, fixed-size and EBML lacing plus ordinary non-laced blocks;
* a BlockGroup with BlockDuration and ReferenceBlock;
* AVC CodecPrivate (AVCDecoderConfigurationRecord) and AAC AudioSpecificConfig.

No external media or network access is involved: every payload is a small
deterministic byte string.
"""

import os

from mkvmp4.ebml import schema
from mkvmp4.ebml.vint import encode_element_id, encode_unknown_size, encode_vint

#: TimestampScale ticks per millisecond (default 1,000,000 ns = 1 ms).
TICKS_PER_MS = 1


def _element(element_id: int, payload: bytes = b"") -> bytes:
    return encode_element_id(element_id) + encode_vint(len(payload)) + payload


def _unknown_master(element_id: int, payload: bytes, width: int = 1) -> bytes:
    return encode_element_id(element_id) + encode_unknown_size(width) + payload


def _uint(element_id: int, value: int) -> bytes:
    length = max(1, (value.bit_length() + 7) // 8)
    return _element(element_id, value.to_bytes(length, "big"))


def _float(element_id: int, value: float) -> bytes:
    import struct
    return _element(element_id, struct.pack(">d", value))


def _string(element_id: int, value: str) -> bytes:
    return _element(element_id, value.encode("ascii"))


def _simple_block(track_number: int, relative_tc: int, flags: int,
                  frames: list[bytes], lacing: int | None = None) -> bytes:
    body = encode_vint(track_number)
    body += relative_tc.to_bytes(2, byteorder="big", signed=True)
    if lacing is None:
        body += bytes([flags])
        body += frames[0]
        return _element(schema.SIMPLE_BLOCK, body)
    frame_count = len(frames)
    block_flags = (flags & ~0x06) | ((lacing & 0x03) << 1)
    body += bytes([block_flags])
    body += bytes([frame_count - 1])
    if lacing == 1:  # Xiph
        for frame in frames[:-1]:
            remaining = len(frame)
            while remaining >= 255:
                body += bytes([255])
                remaining -= 255
            body += bytes([remaining])
    elif lacing == 2:  # fixed size: no size header
        pass
    elif lacing == 3:  # EBML
        sizes = [len(f) for f in frames[:-1]]
        first = sizes[0]
        body += encode_vint((first << 1) ^ (first >> 31))
        for previous, size in zip(sizes, sizes[1:]):
            delta = size - previous
            unsigned = (delta << 1) ^ (delta >> 31)
            body += encode_vint(unsigned & 0xFFFFFFFF)
    for frame in frames:
        body += frame
    return _element(schema.SIMPLE_BLOCK, body)


def _block_group(track_number: int, relative_tc: int, frames: list[bytes],
                 duration: int, reference: int | None) -> bytes:
    block_body = (encode_vint(track_number) +
                  relative_tc.to_bytes(2, byteorder="big", signed=True) +
                  b"\x00" + frames[0])
    group = _element(schema.BLOCK, block_body)
    group += _uint(schema.BLOCK_DURATION, duration)
    if reference is not None:
        group += _element(
            schema.REFERENCE_BLOCK,
            reference.to_bytes(2, byteorder="big", signed=True))
    return _element(schema.BLOCK_GROUP, group)


def _avc_codec_private() -> bytes:
    # Minimal but structurally valid AVCDecoderConfigurationRecord.
    sps = bytes([0x67, 0x42, 0x00, 0x14, 0xAB])
    pps = bytes([0x68, 0xCE, 0x38, 0x80])
    return (bytes([1, 66, 0, 20, 0xFF, 0xE1]) +
            len(sps).to_bytes(2, "big") + sps +
            bytes([1]) + len(pps).to_bytes(2, "big") + pps)


def _tracks() -> bytes:
    avc_entry = (
        _uint(schema.TRACK_NUMBER, 1) +
        _uint(schema.TRACK_UID, 101) +
        _uint(schema.TRACK_TYPE, schema.TRACK_TYPE_VIDEO) +
        _uint(schema.FLAG_DEFAULT, 1) +
        _uint(schema.FLAG_LACING, 1) +
        _uint(schema.DEFAULT_DURATION, 40_000_000) +
        _string(schema.LANGUAGE_BCP47, "und") +
        _string(schema.CODEC_ID, "V_MPEG4/ISO/AVC") +
        _element(schema.CODEC_PRIVATE, _avc_codec_private()) +
        _element(schema.VIDEO,
                 _uint(schema.PIXEL_WIDTH, 320) +
                 _uint(schema.PIXEL_HEIGHT, 240)))
    aac_entry = (
        _uint(schema.TRACK_NUMBER, 2) +
        _uint(schema.TRACK_UID, 202) +
        _uint(schema.TRACK_TYPE, schema.TRACK_TYPE_AUDIO) +
        _uint(schema.FLAG_DEFAULT, 1) +
        _uint(schema.FLAG_LACING, 1) +
        # 12 ms per AAC frame in the 1,000,000 ns TimestampScale; the gap
        # between samples 5 and 6 is twice this plus an extra 8 ms silence.
        _uint(schema.DEFAULT_DURATION, 12_000_000) +
        _string(schema.LANGUAGE_BCP47, "eng") +
        _string(schema.CODEC_ID, "A_AAC") +
        # AAC-LC, 48 kHz stereo: AudioSpecificConfig 0x12 0x10.
        _element(schema.CODEC_PRIVATE, bytes([0x12, 0x10])) +
        _element(schema.AUDIO,
                 _float(schema.SAMPLING_FREQUENCY, 48000.0) +
                 _uint(schema.CHANNELS, 2)))
    return _element(schema.TRACKS,
                    _element(schema.TRACK_ENTRY, avc_entry) +
                    _element(schema.TRACK_ENTRY, aac_entry))


def _info() -> bytes:
    return _element(schema.INFO,
                    _uint(schema.TIMESTAMP_SCALE, 1_000_000) +
                    _float(schema.DURATION, 300.0) +
                    _string(schema.MUXING_APP, "mkvmp4-fixture") +
                    _string(schema.WRITING_APP, "mkvmp4-fixture"))


def _cluster(timestamp_ms: int, content: bytes) -> bytes:
    header = _uint(schema.TIMESTAMP, timestamp_ms)
    return _unknown_master(schema.CLUSTER, header + content)


def build_fixture_bytes() -> bytes:
    """Return the complete synthetic Matroska file as bytes."""
    ebml = _element(
        schema.EBML,
        _uint(schema.EBML_READ_VERSION, 1) +
        _string(schema.EBML_DOC_TYPE, "matroska") +
        _uint(schema.EBML_DOC_TYPE_VERSION, 4))

    # Video PTS sequence (absolute ms): 100,140,120,160,180,220 — the 120
    # arriving after 140 gives B-frame reordering. Clusters start at 80/160.
    c1 = bytearray()
    # Audio Xiph lacing: 3 frames at abs 80,92,104 (cluster-relative 0,12,24).
    c1 += _simple_block(2, 0, 0x00,
                        [b"a0xxxx", b"a1xxxx", b"a2xxxx"], lacing=1)
    # Video keyframe at abs 100 -> relative +20.
    c1 += _simple_block(1, 20, 0x80, [b"\x00\x00\x00\x01keyframe"])
    # Second video frame at abs 140 -> relative +60 (P frame, not key).
    c1 += _simple_block(1, 60, 0x00, [b"\x00\x00\x00\x01pframe0"])

    c2 = bytearray()
    # B frame at abs 120 expressed with a NEGATIVE relative timecode (-40).
    c2 += _simple_block(1, -40, 0x00, [b"\x00\x00\x00\x01bframe0"])
    # Audio fixed-size lacing: 2 equal frames at abs 116,128 (cluster
    # relative -44,-32). The next audio sample is at 160, so the timeline
    # retains a 32 ms silent gap between this block and the following one.
    c2 += _simple_block(2, -44, 0x00,
                        [b"fix000", b"fix001"], lacing=2)
    # Video frame at abs 160 (relative 0) inside a BlockGroup with duration
    # and a ReferenceBlock, marking it as a non-key predicted frame.
    c2 += _block_group(1, 0, [b"\x00\x00\x00\x01pframe1"],
                       duration=40, reference=-40)

    c3 = bytearray()
    # Audio EBML lacing: 2 frames at abs 160,172 (cluster relative 0,+12),
    # immediately after the 32 ms gap (previous sample ended at 128).
    c3 += _simple_block(2, 0, 0x00, [b"ebml0000", b"ebml0001"], lacing=3)
    # Non-laced audio frames at abs 184,196 (relative 24,36) and a final one
    # at abs 272 (relative 112); the jump to 272 keeps a second silent gap.
    c3 += _simple_block(2, 24, 0x00, [b"plain0"])
    c3 += _simple_block(2, 36, 0x00, [b"plain1"])
    c3 += _simple_block(2, 112, 0x00, [b"plain2"])
    # Remaining video frames at abs 180,220.
    c3 += _simple_block(1, 20, 0x00, [b"\x00\x00\x00\x01pframe2"])
    c3 += _simple_block(1, 60, 0x80, [b"\x00\x00\x00\x01keyframe1"])

    segment_payload = (_info() + _tracks() +
                       _cluster(80, bytes(c1)) +
                       _cluster(160, bytes(c2)) +
                       _cluster(160, bytes(c3)))
    # The Segment uses a *known* length so that exactly the three Clusters
    # contribute to the reported unknown_size_masters count.
    segment = _element(schema.SEGMENT, segment_payload)
    return ebml + segment


def write_fixture(path: str) -> str:
    """Write the fixture to ``path`` and return the path."""
    payload = build_fixture_bytes()
    directory = os.path.dirname(os.path.abspath(path))
    os.makedirs(directory, exist_ok=True)
    with open(path, "wb") as handle:
        handle.write(payload)
    return path


def truncate_copy(source: str, target: str, cut_bytes: int) -> str:
    """Write a truncated copy for corruption tests."""
    with open(source, "rb") as handle:
        data = handle.read()
    cut = max(0, len(data) - cut_bytes)
    with open(target, "wb") as handle:
        handle.write(data[:cut])
    return target
