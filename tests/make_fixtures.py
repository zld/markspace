"""Generate small synthetic Matroska fixtures for the test/demo pipeline.

Nothing here references external media: every byte of codec data is
synthesized. The generator deliberately exercises the hard cases the
remuxer must survive:

* an unknown-length Segment (all-ones size VINT);
* at least one unknown-length Cluster;
* video and audio blocks interleaved across multiple Clusters;
* a B-frame reorder pattern (negative relative block timecodes);
* a duplicate presentation timestamp;
* a missing Cues index (the default);
* optional AAC and Opus audio tracks and an optional subtitle track used
  to demonstrate explicit codec rejection.
"""

from __future__ import annotations

import struct

# --- EBML encoding primitives ----------------------------------------------

def encode_vint(value: int, width: int) -> bytes:
    """Encode ``value`` as a fixed-width EBML VINT."""
    if not 1 <= width <= 8:
        raise ValueError("VINT width out of range")
    capacity = (1 << (7 * width)) - 2
    if not 0 <= value <= capacity:
        raise ValueError(f"value {value} does not fit in {width}-byte VINT")
    marker = 1 << (8 * width - width)
    return (marker | value).to_bytes(width, "big")


def encode_id(element_id: int) -> bytes:
    """Encode an EBML element ID.

    IDs are VINTs whose length marker is part of the assigned value (e.g.
    0x4286 is the fixed two-byte form), so the width is derived from the
    marker position and the ID is emitted verbatim.
    """
    width = max(1, (element_id.bit_length() + 7) // 8)
    marker = 1 << (8 * width - width)
    if not element_id & marker:
        raise ValueError(f"0x{element_id:X} is not a valid {width}-byte ID")
    return element_id.to_bytes(width, "big")


def encode_size(size: int, *, unknown: bool = False,
                width: int | None = None) -> bytes:
    if unknown:
        if width is None:
            raise ValueError("unknown-length VINT needs an explicit width")
        return b"\xff" * width
    if width is None:
        width = 1
        while width < 8 and size > (1 << (7 * width)) - 2:
            width += 1
    return encode_vint(size, width)


def element(element_id: int, payload: bytes,
            *, unknown: bool = False,
            size_width: int | None = None) -> bytes:
    return (encode_id(element_id)
            + encode_size(len(payload), unknown=unknown,
                          width=size_width)
            + payload)


def uint_element(element_id: int, value: int) -> bytes:
    if value == 0:
        body = b""
    else:
        body = value.to_bytes(max(1, (value.bit_length() + 7) // 8), "big")
    return element(element_id, body)


def string_element(element_id: int, value: str) -> bytes:
    return element(element_id, value.encode("utf-8"))


def float_element(element_id: int, value: float) -> bytes:
    return element(element_id, struct.pack(">d", value))


def binary_element(element_id: int, value: bytes) -> bytes:
    return element(element_id, value)


# --- codec private data synthesis ------------------------------------------

class _BitWriter:
    def __init__(self) -> None:
        self.bits: list[int] = []

    def put(self, value: int, width: int) -> None:
        for i in range(width - 1, -1, -1):
            self.bits.append((value >> i) & 1)

    def ue(self, value: int) -> None:
        code = value + 1
        width = code.bit_length()
        self.bits.extend([0] * (width - 1))
        self.put(code, width)

    def bytes(self) -> bytes:
        self.put(1, 1)  # RBSP stop bit
        while len(self.bits) % 8:
            self.put(0, 1)
        out = bytearray()
        for i in range(0, len(self.bits), 8):
            out.append(sum(
                bit << (7 - j) for j, bit in enumerate(self.bits[i:i + 8])))
        return bytes(out)


def _with_emulation_prevention(nal: bytes) -> bytes:
    out = bytearray()
    zeros = 0
    for byte in nal:
        if zeros >= 2 and byte <= 3:
            out.append(3)
            zeros = 0
        out.append(byte)
        zeros = zeros + 1 if byte == 0 else 0
    return bytes(out)


def make_sps(width_mbs: int, height_map: int) -> bytes:
    """Baseline-profile SPS for ``16*width_mbs`` x ``16*height_map`` pixels."""
    bw = _BitWriter()
    bw.put(66, 8)      # profile_idc Baseline
    bw.put(0, 8)       # constraint flags / reserved
    bw.put(30, 8)      # level_idc 3.0
    bw.ue(0)           # seq_parameter_set_id
    bw.ue(0)           # log2_max_frame_num_minus4
    bw.ue(0)           # pic_order_cnt_type
    bw.ue(0)           # log2_max_pic_order_cnt_lsb_minus4
    bw.ue(1)           # max_num_ref_frames
    bw.put(0, 1)       # gaps_in_frame_num_allowed
    bw.ue(width_mbs - 1)
    bw.ue(height_map - 1)
    bw.put(1, 1)       # frame_mbs_only_flag
    bw.put(0, 1)       # direct_8x8_inference_flag
    bw.put(0, 1)       # frame_cropping_flag
    bw.put(1, 1)       # vui_parameters_present_flag
    bw.put(0, 1)       # no vui payload
    rbsp = bw.bytes()
    return b"\x67" + _with_emulation_prevention(rbsp)


def make_pps() -> bytes:
    bw = _BitWriter()
    bw.ue(0)           # pic_parameter_set_id
    bw.ue(0)           # seq_parameter_set_id
    bw.put(0, 1)       # entropy_coding_mode_flag (CAVLC)
    bw.put(0, 1)       # bottom_field_pic_order_in_frame_present
    bw.ue(0)           # num_slice_groups_minus1
    bw.ue(0)           # num_ref_idx_l0_default_active_minus1
    bw.ue(0)           # num_ref_idx_l1_default_active_minus1
    bw.put(0, 1)       # weighted_pred_flag
    bw.put(0, 2)       # weighted_bipred_idc
    bw.se(0)           # pic_init_qp_minus26 (signed exp-Golomb)
    bw.se(0)           # pic_init_qs_minus26
    bw.se(0)           # chroma_qp_index_offset
    bw.put(1, 1)       # deblocking_filter_control_present
    bw.put(0, 1)       # constrained_intra_pred
    bw.put(0, 1)       # redundant_pic_cnt_present
    return b"\x68" + _with_emulation_prevention(bw.bytes())


def _se(self: _BitWriter, value: int) -> None:
    mapped = 2 * value - 1 if value > 0 else -2 * value
    self.ue(mapped)


_BitWriter.se = _se  # type: ignore[attr-defined]


def make_avcc(width_mbs: int, height_map: int) -> bytes:
    sps = make_sps(width_mbs, height_map)
    pps = make_pps()
    return (bytes([1, 66, 0, 30, 0xFF, 0xE1])
            + len(sps).to_bytes(2, "big") + sps
            + bytes([1]) + len(pps).to_bytes(2, "big") + pps)


def make_aac_config() -> bytes:
    # AAC-LC (AOT 2), 48 kHz (index 3), 2 channels: 5+4+4 = 13 bits.
    # The fields form one continuous bitstream; pad the 13-bit value with
    # three zero bits on the right to reach 16 bits.
    value = (2 << 8) | (3 << 4) | 2   # 13 significant bits
    return (value << 3).to_bytes(2, "big")


def make_opus_head() -> bytes:
    return (b"OpusHead" + bytes([1, 2]) + (312).to_bytes(2, "little")
            + (48000).to_bytes(4, "little")
            + (0).to_bytes(2, "little", signed=True) + bytes([0]))


# --- Matroska element IDs used by the fixture ------------------------------

ID_EBML_HEADER = 0x1A45DFA3
ID_EBML_VERSION = 0x4286
ID_EBML_READ_VERSION = 0x42F7
ID_EBML_MAX_ID_LENGTH = 0x42F2
ID_EBML_MAX_SIZE_LENGTH = 0x42F3
ID_DOC_TYPE = 0x4282
ID_DOC_TYPE_VERSION = 0x4287
ID_DOC_TYPE_READ_VERSION = 0x4285
ID_SEGMENT = 0x18538067
ID_INFO = 0x1549A966
ID_TIMECODE_SCALE = 0x2AD7B1
ID_MUXING_APP = 0x4D80
ID_TRACKS = 0x1654AE6B
ID_TRACK_ENTRY = 0xAE
ID_TRACK_NUMBER = 0xD7
ID_TRACK_UID = 0x73C5
ID_TRACK_TYPE = 0x83
ID_FLAG_DEFAULT = 0x88
ID_FLAG_LACING = 0x9C
ID_DEFAULT_DURATION = 0x23E383
ID_LANGUAGE_BCP47 = 0x22B59D
ID_CODEC_ID = 0x86
ID_CODEC_PRIVATE = 0x63A2
ID_AUDIO = 0xE1
ID_VIDEO = 0xE0
ID_SAMPLING_FREQ = 0xB5
ID_CHANNELS = 0x9F
ID_PIXEL_WIDTH = 0xB0
ID_PIXEL_HEIGHT = 0xBA
ID_CLUSTER = 0x1F43B675
ID_CLUSTER_TIMECODE = 0xE7
ID_SIMPLE_BLOCK = 0xA3
ID_PREV_SIZE = 0xAB


def make_simple_block(track_number: int, relative_tc: int,
                      keyframe: bool, frame: bytes) -> bytes:
    header = encode_vint(track_number, 1)
    header += relative_tc.to_bytes(2, "big", signed=True)
    header += bytes([0x80 if keyframe else 0x00])
    return element(ID_SIMPLE_BLOCK, header + frame)


def make_nal_frame(nal_type: int, payload_size: int,
                  frame_index: int) -> bytes:
    """One length-prefixed NAL unit; payload is deterministic filler."""
    nal = bytes([nal_type & 0x1F]) + bytes(
        (frame_index * 7 + i) & 0xFF for i in range(payload_size - 1))
    return len(nal).to_bytes(4, "big") + nal


def _track_entry(number: int, uid: int, ttype: int, codec_id: str,
                 codec_private: bytes | None, *, default: bool = True,
                 default_duration_ns: int | None = None,
                 language: str = "",
                 audio: tuple[float, int] | None = None,
                 video: tuple[int, int] | None = None) -> bytes:
    payload = (uint_element(ID_TRACK_NUMBER, number)
               + uint_element(ID_TRACK_UID, uid)
               + uint_element(ID_TRACK_TYPE, ttype)
               + uint_element(ID_FLAG_DEFAULT, 1 if default else 0)
               + uint_element(ID_FLAG_LACING, 0)
               + string_element(ID_CODEC_ID, codec_id))
    if default_duration_ns is not None:
        payload += uint_element(ID_DEFAULT_DURATION, default_duration_ns)
    if language:
        payload += string_element(ID_LANGUAGE_BCP47, language)
    if codec_private is not None:
        payload += binary_element(ID_CODEC_PRIVATE, codec_private)
    if audio is not None:
        rate, channels = audio
        payload += element(ID_AUDIO,
                           float_element(ID_SAMPLING_FREQ, rate)
                           + uint_element(ID_CHANNELS, channels))
    if video is not None:
        width, height = video
        payload += element(ID_VIDEO,
                           uint_element(ID_PIXEL_WIDTH, width)
                           + uint_element(ID_PIXEL_HEIGHT, height))
    return element(ID_TRACK_ENTRY, payload)




# --- document assembly ------------------------------------------------------

def make_ebml_header() -> bytes:
    payload = (uint_element(ID_EBML_VERSION, 1)
               + uint_element(ID_EBML_READ_VERSION, 1)
               + uint_element(ID_EBML_MAX_ID_LENGTH, 4)
               + uint_element(ID_EBML_MAX_SIZE_LENGTH, 8)
               + string_element(ID_DOC_TYPE, "matroska")
               + uint_element(ID_DOC_TYPE_VERSION, 4)
               + uint_element(ID_DOC_TYPE_READ_VERSION, 2))
    return element(ID_EBML_HEADER, payload)


def make_info(timestamp_scale_ns: int) -> bytes:
    payload = (uint_element(ID_TIMECODE_SCALE, timestamp_scale_ns)
               + string_element(ID_MUXING_APP, "mkvmp4-fixture"))
    return element(ID_INFO, payload)


def make_hvcc() -> bytes:
    """Minimal HEVC decoder configuration record with VPS/SPS/PPS arrays.

    The NAL payloads are tagged filler but structurally complete: the remuxer
    validates the hvcC container (array types and lengths), not bitstream
    internals. Layout per ISO/IEC 14496-15 8.3.3.1.
    """
    header = bytearray(23)
    header[0] = 1                       # configurationVersion
    header[1] = 1                       # general_profile_space(0)+idc
    header[12] = 0xF0 | 1              # general... level idc low bits
    header[21] = 0xFC | 3             # lengthSizeMinusOne = 3 (4 bytes)
    header[22] = 3                    # numOfArrays
    body = bytearray(header)

    def array(nal_type: int, nal: bytes) -> bytes:
        return (bytes([0x80 | nal_type]) + (1).to_bytes(2, "big")
                + len(nal).to_bytes(2, "big") + nal)

    body += array(32, bytes([0x40]) + b"vps-fixture")
    body += array(33, bytes([0x42]) + b"sps-fixture")
    body += array(34, bytes([0x44]) + b"pps-fixture")
    return bytes(body)


def make_tracks(video_codec: str, include_opus: bool,
                include_subtitle: bool, width: int,
                height: int) -> bytes:
    if video_codec == "V_MPEGH/ISO/HEVC":
        private = make_hvcc()
    else:
        private = make_avcc(width // 16, height // 16)
    entries = _track_entry(
        1, 1001, 1, video_codec, private, default=True,
        default_duration_ns=40_000_000, language="und",
        video=(width, height))
    entries += _track_entry(
        2, 1002, 2, "A_AAC", make_aac_config(), default=True,
        language="eng", audio=(48000.0, 2))
    next_number = 3
    if include_opus:
        entries += _track_entry(
            next_number, 1003, 2, "A_OPUS", make_opus_head(),
            default=False, language="fra", audio=(48000.0, 2))
        next_number += 1
    if include_subtitle:
        entries += _track_entry(
            next_number, 1004, 17, "S_TEXT/UTF8", None,
            default=False, language="und")
    return element(ID_TRACKS, entries)


def make_cluster(timecode_ms: int, blocks: list[bytes],
                 *, unknown_length: bool) -> bytes:
    payload = uint_element(ID_CLUSTER_TIMECODE, timecode_ms) + b"".join(blocks)
    if unknown_length:
        return encode_id(ID_CLUSTER) + encode_size(0, unknown=True,
                                                   width=8) + payload
    return element(ID_CLUSTER, payload)


def build_fixture(*, video_frames: int = 20, width: int = 320,
                  height: int = 240, video_codec: str = "V_MPEG4/ISO/AVC",
                  include_opus: bool = True,
                  include_subtitle: bool = False,
                  unknown_segment: bool = True) -> bytes:
    """Assemble the synthetic Matroska document.

    Timing is in milliseconds (TimestampScale 1,000,000 ns). Video runs at
    25 fps in groups of five decode positions; the last position of each
    group is a B-frame whose presentation slot is swapped with the previous
    one, giving valid positive/negative relative timecodes within one
    cluster. One duplicate presentation timestamp is injected. Audio is AAC
    at 48 kHz in 1024-sample packets (~21.33 ms) plus 20 ms Opus packets.
    """
    parts = [make_ebml_header()]
    info_tracks = make_info(1_000_000) + make_tracks(
        video_codec, include_opus, include_subtitle, width, height)

    # (absolute PTS ms, relative TC inside its cluster, keyframe, NAL bytes)
    video_blocks: list[tuple[int, int, bool, bytes]] = []
    frame_index = 0
    group = 5
    for n in range(video_frames):
        slot = n % group
        cluster_index = n // group
        base_ms = cluster_index * group * 40
        if slot == group - 1:
            # B-frame decoded last, presented one slot earlier: PTS at
            # base + (group-2)*40, relative TC = (group-2)*40.
            present_slot = group - 2
            rel = present_slot * 40
            nal_type = 61  # TRAIL_R / non-reference marker for filler
            keyframe = False
        elif slot == group - 2:
            # Frame decoded before the B-frame but presented in its slot.
            present_slot = group - 1
            rel = present_slot * 40
            nal_type = 65
            keyframe = (cluster_index == 0)
        else:
            present_slot = slot
            rel = slot * 40
            nal_type = 65
            keyframe = (slot == 0)
        pts_ms = base_ms + present_slot * 40
        # Duplicate PTS: frame 6 repeats frame 5's presentation timestamp.
        if n == 6:
            pts_ms = 5 * 40
            rel = pts_ms - base_ms
        nal = make_nal_frame(nal_type, 48, frame_index)
        frame_index += 1
        video_blocks.append((pts_ms, rel, keyframe, nal))

    audio_packets = int(video_frames * 40 / (1024 / 48))
    audio_blocks: list[tuple[int, bytes]] = []
    for n in range(audio_packets):
        pts_ms = int(round(n * (1024 / 48)))
        packet = bytes([0x21]) + bytes(
            (n * 13 + i) & 0xFF for i in range(24))
        audio_blocks.append((pts_ms, packet))

    opus_blocks: list[tuple[int, bytes]] = []
    if include_opus:
        opus_packets = int(video_frames * 40 / 20)
        for n in range(opus_packets):
            pts_ms = n * 20
            # RFC 6716 TOC config 17 = CELT-only 20 ms, code 0 = one frame.
            packet = bytes([(17 << 3)]) + bytes(
                (n * 17 + i) & 0xFF for i in range(16))
            opus_blocks.append((pts_ms, packet))

    cluster_span_ms = group * 40  # five video frames per cluster
    max_ms = video_frames * 40
    cluster_payloads: list[bytes] = []
    unknown_seen = False
    for cluster_index, base_ms in enumerate(
            range(0, max_ms, cluster_span_ms)):
        end_ms = base_ms + cluster_span_ms
        body: list[bytes] = []
        # Video blocks belong to a cluster by decode position, never by
        # (possibly reordered) presentation timestamp.
        lo = cluster_index * group
        hi = min(lo + group, video_frames)
        for n in range(lo, hi):
            _pts_ms, rel, key, nal = video_blocks[n]
            body.append(make_simple_block(1, rel, key, nal))
        for pts_ms, packet in audio_blocks:
            if base_ms <= pts_ms < end_ms:
                body.append(make_simple_block(2, pts_ms - base_ms, True,
                                              packet))
        for pts_ms, packet in opus_blocks:
            if base_ms <= pts_ms < end_ms:
                body.append(make_simple_block(3, pts_ms - base_ms, True,
                                              packet))
        # The second cluster is unknown length on purpose.
        is_unknown = (cluster_index == 1) and not unknown_seen
        unknown_seen = unknown_seen or is_unknown
        cluster_payloads.append(
            make_cluster(base_ms, body, unknown_length=is_unknown))

    segment_body = info_tracks + b"".join(cluster_payloads)
    if unknown_segment:
        segment = (encode_id(ID_SEGMENT)
                   + encode_size(0, unknown=True, width=8) + segment_body)
    else:
        segment = element(ID_SEGMENT, segment_body)
    parts.append(segment)
    return b"".join(parts)


def main(argv: list[str] | None = None) -> int:
    import argparse
    import json
    import os

    parser = argparse.ArgumentParser(
        description="Generate a synthetic Matroska remuxer fixture")
    parser.add_argument("--output", required=True)
    parser.add_argument("--frames", type=int, default=20)
    parser.add_argument("--width", type=int, default=320)
    parser.add_argument("--height", type=int, default=240)
    parser.add_argument("--codec", choices=("avc", "hevc"), default="avc")
    parser.add_argument("--no-opus", action="store_true")
    parser.add_argument("--with-subtitle", action="store_true")
    parser.add_argument("--known-length-segment", action="store_true")
    args = parser.parse_args(argv)

    video_codec = ("V_MPEGH/ISO/HEVC" if args.codec == "hevc"
                   else "V_MPEG4/ISO/AVC")
    data = build_fixture(
        video_frames=args.frames, width=args.width, height=args.height,
        video_codec=video_codec, include_opus=not args.no_opus,
        include_subtitle=args.with_subtitle,
        unknown_segment=not args.known_length_segment)
    out_dir = os.path.dirname(os.path.abspath(args.output)) or "."
    os.makedirs(out_dir, exist_ok=True)
    with open(args.output, "wb") as handle:
        handle.write(data)
    print(json.dumps({"output": args.output, "bytes": len(data)},
                     indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
