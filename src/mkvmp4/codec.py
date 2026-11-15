"""Codec mapping: Matroska CodecID -> MP4 sample entries, with strict checks.

Every accepted track yields a :class:`CodecMapping` describing the exact
sample-entry box and decoder configuration blob to emit. Anything that
cannot be expressed safely in ISO BMFF raises :class:`CodecRejectedError`
with a track-scoped explanation - tracks are never silently dropped.

Supported:
    V_MPEG4/ISO/AVC   -> avc1 + avcC (AVC decoder configuration record)
    V_MPEGH/ISO/HEVC  -> hvc1 + hvcC (HEVC decoder configuration record)
    A_AAC             -> mp4a + esds (AudioSpecificConfig)
    A_OPUS            -> Opus + dOps (OpusHead)
Subtitles and every other CodecID are rejected explicitly.
"""

from __future__ import annotations

from dataclasses import dataclass

from .errors import CodecRejectedError
from .matroska import Track, TRACK_TYPE_AUDIO, TRACK_TYPE_VIDEO, TRACK_TYPE_SUBTITLE

CODEC_AVC = "V_MPEG4/ISO/AVC"
CODEC_HEVC = "V_MPEGH/ISO/HEVC"
CODEC_AAC = "A_AAC"
CODEC_OPUS = "A_OPUS"


@dataclass
class CodecMapping:
    track_number: int
    handler_type: str          # 'vide' or 'soun'
    entry_type: bytes          # avc1 / hvc1 / mp4a / Opus
    config_box_type: bytes     # avcC / hvcC / esds / dOps
    config_payload: bytes      # box payload (without box header)
    timescale: int
    width: int = 0
    height: int = 0
    channels: int = 0
    sample_rate: int = 0
    sample_size: int = 0       # bits per sample, audio only
    nalu_length_size: int = 0  # video only


class _BitReader:
    """Tiny big-endian bit reader for the AVC SPS subset we need."""

    def __init__(self, data: bytes) -> None:
        self.data = data
        self.pos = 0

    def bits(self, n: int) -> int:
        value = 0
        for _ in range(n):
            byte = self.data[self.pos >> 3]
            bit = (byte >> (7 - (self.pos & 7))) & 1
            self.pos += 1
            value = (value << 1) | bit
        return value

    def ue(self) -> int:
        zeros = 0
        while self.bits(1) == 0:
            zeros += 1
            if zeros > 32:
                raise CodecRejectedError("exp-Golomb code too long in SPS")
        return (1 << zeros) - 1 + (self.bits(zeros) if zeros else 0)

    def se(self) -> int:
        code = self.ue()
        return (code + 1) // 2 if code & 1 else -(code // 2)


def _map_avc(track: Track) -> CodecMapping:
    if not track.codec_private or len(track.codec_private) < 7:
        raise CodecRejectedError(
            f"track {track.number}: AVC track needs an avcC CodecPrivate")
    cfg = track.codec_private
    if cfg[0] != 1:
        raise CodecRejectedError(
            f"track {track.number}: unsupported avcC configurationVersion "
            f"{cfg[0]}")
    nalu_length_size = (cfg[4] & 0x03) + 1
    if nalu_length_size != 4:
        raise CodecRejectedError(
            f"track {track.number}: only 4-byte NAL length prefixes are "
            f"expressible (got {nalu_length_size})")
    sps_count = cfg[5] & 0x1F
    if sps_count == 0:
        raise CodecRejectedError(
            f"track {track.number}: avcC contains no SPS parameter set")
    offset = 6
    sps_list = []
    for _ in range(sps_count):
        if offset + 2 > len(cfg):
            raise CodecRejectedError("truncated avcC SPS length")
        length = int.from_bytes(cfg[offset:offset + 2], "big")
        offset += 2
        sps_list.append(cfg[offset:offset + length])
        offset += length
    pps_count = cfg[offset] if offset < len(cfg) else 0
    if pps_count == 0:
        raise CodecRejectedError(
            f"track {track.number}: avcC contains no PPS parameter set")
    width, height = _avc_dimensions(sps_list[0])
    if track.pixel_width and track.pixel_width != width:
        raise CodecRejectedError(
            f"track {track.number}: TrackEntry width {track.pixel_width} "
            f"!= SPS width {width}")
    if track.pixel_height and track.pixel_height != height:
        raise CodecRejectedError(
            f"track {track.number}: TrackEntry height {track.pixel_height} "
            f"!= SPS height {height}")
    return CodecMapping(
        track_number=track.number, handler_type="vide",
        entry_type=b"avc1", config_box_type=b"avcC",
        config_payload=cfg, timescale=0, width=width, height=height,
        nalu_length_size=nalu_length_size)


def _avc_dimensions(sps: bytes) -> tuple[int, int]:
    """Parse enough of an AVC SPS to recover cropped picture dimensions."""
    if len(sps) < 4 or sps[0] & 0x1F != 7:
        raise CodecRejectedError("avcC SPS NAL unit has wrong type")
    # Remove emulation prevention bytes for bitstream parsing.
    rbsp = bytearray()
    zeros = 0
    for byte in sps[1:]:
        if zeros >= 2 and byte == 3:
            zeros = 0
            continue
        rbsp.append(byte)
        zeros = zeros + 1 if byte == 0 else 0
    br = _BitReader(bytes(rbsp))
    profile_idc = br.bits(8)
    br.bits(8)  # constraint flags + reserved
    br.bits(8)  # level_idc
    br.ue()     # seq_parameter_set_id
    if profile_idc in (100, 110, 122, 244, 44, 83, 86, 118, 128, 138,
                       139, 134, 135):
        chroma = br.ue()
        if chroma == 3:
            br.bits(1)
        br.ue()  # bit_depth_luma_minus8
        br.ue()  # bit_depth_chroma_minus8
        br.bits(1)  # qpprime_y_zero_transform_bypass
        if br.bits(1):  # seq_scaling_matrix_present
            for i in range(8 if chroma != 3 else 12):
                if br.bits(1):
                    size = 16 if i < 6 else 64
                    last = next_ = 8
                    for _ in range(size):
                        if next_:
                            delta = br.se()
                            next_ = (last + delta) % 256
                        last = next_ if next_ else last
    br.ue()  # log2_max_frame_num_minus4
    poc_type = br.ue()
    if poc_type == 0:
        br.ue()
    elif poc_type == 1:
        br.bits(1)
        br.se()
        br.se()
        for _ in range(br.ue()):
            br.se()
    br.ue()  # max_num_ref_frames
    br.bits(1)  # gaps_in_frame_num_allowed
    width_mbs = br.ue() + 1
    height_map = br.ue() + 1
    frame_mbs_only = br.bits(1)
    if not frame_mbs_only:
        br.bits(1)  # mb_adaptive_frame_field
    br.bits(1)  # direct_8x8_inference
    crop_l = crop_r = crop_t = crop_b = 0
    if br.bits(1):  # frame_cropping
        crop_l = br.ue()
        crop_r = br.ue()
        crop_t = br.ue()
        crop_b = br.ue()
    width = width_mbs * 16 - (crop_l + crop_r) * 2
    height = (2 - frame_mbs_only) * height_map * 16 \
        - (crop_t + crop_b) * 2 * (2 - frame_mbs_only)
    if width <= 0 or height <= 0:
        raise CodecRejectedError("SPS yields non-positive picture dimensions")
    return width, height


def _map_hevc(track: Track) -> CodecMapping:
    if not track.codec_private or len(track.codec_private) < 23:
        raise CodecRejectedError(
            f"track {track.number}: HEVC track needs an hvcC CodecPrivate")
    cfg = track.codec_private
    if cfg[0] != 1:
        raise CodecRejectedError(
            f"track {track.number}: unsupported hvcC configurationVersion "
            f"{cfg[0]}")
    nalu_length_size = (cfg[21] & 0x03) + 1
    if nalu_length_size != 4:
        raise CodecRejectedError(
            f"track {track.number}: only 4-byte NAL length prefixes are "
            f"expressible (got {nalu_length_size})")
    num_arrays = cfg[22]
    if num_arrays == 0:
        raise CodecRejectedError("hvcC contains no parameter-set arrays")
    offset = 23
    types_seen: set[int] = set()
    for _ in range(num_arrays):
        if offset + 3 > len(cfg):
            raise CodecRejectedError("truncated hvcC array header")
        nal_type = cfg[offset] & 0x3F
        count = int.from_bytes(cfg[offset + 1:offset + 3], "big")
        offset += 3
        types_seen.add(nal_type)
        for _ in range(count):
            if offset + 2 > len(cfg):
                raise CodecRejectedError("truncated hvcC NAL length")
            length = int.from_bytes(cfg[offset:offset + 2], "big")
            offset += 2 + length
    if offset != len(cfg):
        raise CodecRejectedError("hvcC trailing bytes after parameter arrays")
    # VPS=32, SPS=33, PPS=34 are mandatory for a decodable HEVC stream.
    missing = {32, 33, 34} - types_seen
    if missing:
        raise CodecRejectedError(
            f"hvcC missing mandatory NAL array types {sorted(missing)}")
    if not track.pixel_width or not track.pixel_height:
        raise CodecRejectedError(
            f"track {track.number}: HEVC TrackEntry pixel dimensions "
            "are required")
    return CodecMapping(
        track_number=track.number, handler_type="vide",
        entry_type=b"hvc1", config_box_type=b"hvcC",
        config_payload=cfg, timescale=0,
        width=track.pixel_width, height=track.pixel_height,
        nalu_length_size=nalu_length_size)


def validate_video_block(mapping: CodecMapping, data: bytes) -> None:
    """Walk length-prefixed NAL units and reject any structural mismatch.

    Every block must consist solely of complete NAL units: the sum of the
    prefixes and declared lengths must equal the block size exactly, so no
    arbitrary byte can ever be carried into the MP4 sample.
    """
    prefix = mapping.nalu_length_size
    pos = 0
    found = False
    while pos < len(data):
        if pos + prefix > len(data):
            raise CodecRejectedError(
                f"track {mapping.track_number}: truncated NAL length prefix")
        length = int.from_bytes(data[pos:pos + prefix], "big")
        pos += prefix
        if length == 0:
            raise CodecRejectedError(
                f"track {mapping.track_number}: zero-length NAL unit")
        if pos + length > len(data):
            raise CodecRejectedError(
                f"track {mapping.track_number}: NAL unit length {length} "
                "crosses block boundary")
        pos += length
        found = True
    if not found:
        raise CodecRejectedError(
            f"track {mapping.track_number}: video block contains no NAL unit")


# --- AAC --------------------------------------------------------------------

# Sample rate index table from ISO/IEC 14496-3 Table 1.16.
AAC_RATES = (96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000,
             12000, 11025, 8000, 7350)
AAC_OBJECT_TYPES = {1: "AAC Main", 2: "AAC LC", 3: "AAC SSR", 4: "AAC LTP",
                    5: "SBR", 6: "AAC Scalable", 17: "ER LC", 23: "LD",
                    29: "PS"}


def _ber_length(length: int) -> bytes:
    encoded = [length & 0x7F]
    length >>= 7
    while length:
        encoded.append(0x80 | (length & 0x7F))
        length >>= 7
    return bytes(reversed(encoded))


def _descriptor(tag: int, payload: bytes) -> bytes:
    return bytes([tag]) + _ber_length(len(payload)) + payload


def _parse_aac_config(track: Track) -> tuple[int, int, int, int]:
    # AudioSpecificConfig uses fixed-width fields (ISO/IEC 14496-3 1.6.2.1),
    # not exp-Golomb coding.
    if not track.codec_private or len(track.codec_private) < 2:
        raise CodecRejectedError(
            f"track {track.number}: AAC track needs AudioSpecificConfig")
    cfg = track.codec_private
    br = _BitReader(cfg)
    aot = br.bits(5)
    if aot == 31:
        aot = 32 + br.bits(6)
    first_aot = aot
    if aot not in (1, 2, 3, 4, 5, 6, 17, 23, 29):
        raise CodecRejectedError(
            f"track {track.number}: unsupported AAC object type {aot}")
    freq_index = br.bits(4)
    if freq_index == 0xF:
        rate = br.bits(24)
    elif freq_index < len(AAC_RATES):
        rate = AAC_RATES[freq_index]
    else:
        raise CodecRejectedError(
            f"track {track.number}: invalid AAC sampling index {freq_index}")
    channels = br.bits(4)
    if not 1 <= channels <= 8:
        raise CodecRejectedError(
            f"track {track.number}: invalid AAC channel configuration "
            f"{channels}")
    # HE-AAC signaling carries an explicit extension sampling rate.
    if first_aot in (5, 29):
        br.bits(5)  # extensionAudioObjectType
        ext_index = br.bits(4)
        if ext_index == 0xF:
            rate = br.bits(24)
        elif ext_index < len(AAC_RATES):
            rate = AAC_RATES[ext_index]
    if track.sample_rate and int(track.sample_rate) != rate:
        raise CodecRejectedError(
            f"track {track.number}: AudioSpecificConfig rate {rate} != "
            f"TrackEntry rate {int(track.sample_rate)}")
    if track.channels and track.channels != channels:
        raise CodecRejectedError(
            f"track {track.number}: AAC config channels {channels} != "
            f"TrackEntry channels {track.channels}")
    return aot, rate, channels, first_aot


def _map_aac(track: Track) -> CodecMapping:
    aot, rate, channels, _ = _parse_aac_config(track)
    if aot not in (2, 5, 29):
        raise CodecRejectedError(
            f"track {track.number}: MP4 only accepts AAC-LC/HE profiles "
            f"({AAC_OBJECT_TYPES.get(aot, aot)} is not expressible)")
    # DecoderConfigDescriptor: objectTypeIndication 0x40 = ISO/IEC 14496-3.
    dcd_payload = (
        bytes([0x40, 0x15, 0x00, 0x00, 0x00])
        + (0).to_bytes(4, "big") + (0).to_bytes(4, "big")
        + _descriptor(0x05, track.codec_private or b"")
    )
    es_payload = (
        (track.number & 0xFFFF).to_bytes(2, "big") + bytes([0x00])
        + _descriptor(0x04, dcd_payload)
        + _descriptor(0x06, bytes([0x02]))
    )
    esds = _descriptor(0x03, es_payload)
    return CodecMapping(
        track_number=track.number, handler_type="soun",
        entry_type=b"mp4a", config_box_type=b"esds",
        config_payload=esds, timescale=rate,
        channels=channels, sample_rate=rate, sample_size=16)


# --- Opus -------------------------------------------------------------------

OPUS_SAMPLE_RATE = 48000
OPUS_MAX_FRAME_SAMPLES = 5760
OPUS_MAPPING_FAMILY_MONO_STEREO = 0


def _map_opus(track: Track) -> CodecMapping:
    head = track.codec_private
    if not head or len(head) < 19:
        raise CodecRejectedError(
            f"track {track.number}: Opus track needs an OpusHead blob")
    if head[:8] != b"OpusHead":
        raise CodecRejectedError(
            f"track {track.number}: OpusHead magic missing")
    version = head[8]
    if version != 1:
        raise CodecRejectedError(
            f"track {track.number}: unsupported OpusHead version {version}")
    channels = head[9]
    if channels not in (1, 2):
        raise CodecRejectedError(
            f"track {track.number}: only 1/2 channel Opus mappings are "
            f"expressible (got {channels})")
    pre_skip = int.from_bytes(head[10:12], "little")
    input_rate = int.from_bytes(head[12:16], "little")
    gain = int.from_bytes(head[16:18], "little", signed=True)
    mapping_family = head[18]
    if mapping_family != OPUS_MAPPING_FAMILY_MONO_STEREO:
        raise CodecRejectedError(
            f"track {track.number}: Opus channel mapping family "
            f"{mapping_family} cannot be expressed in MP4")
    if input_rate not in (8000, 12000, 16000, 24000, 48000):
        raise CodecRejectedError(
            f"track {track.number}: implausible Opus input rate {input_rate}")
    if pre_skip > OPUS_SAMPLE_RATE:
        raise CodecRejectedError(
            f"track {track.number}: Opus pre-skip {pre_skip} out of range")
    if track.channels and track.channels != channels:
        raise CodecRejectedError(
            f"track {track.number}: OpusHead channels {channels} != "
            f"TrackEntry channels {track.channels}")
    # dOps (v0) per RFC 8486 / MP4 Opus registration.
    dops = (
        bytes([version]) + channels.to_bytes(1, "little")
        + pre_skip.to_bytes(2, "little")
        + OPUS_SAMPLE_RATE.to_bytes(4, "little")
        + gain.to_bytes(2, "little", signed=True)
        + mapping_family.to_bytes(1, "little")
    )
    return CodecMapping(
        track_number=track.number, handler_type="soun",
        entry_type=b"Opus", config_box_type=b"dOps",
        config_payload=dops, timescale=OPUS_SAMPLE_RATE,
        channels=channels, sample_rate=OPUS_SAMPLE_RATE, sample_size=16)


def opus_frame_duration(data: bytes) -> int:
    """Return an Opus packet duration in 48kHz samples from its TOC byte.

    Code 3 (arbitrary number of frames) would require CELT/SILK internal
    framing to measure safely and is rejected instead of guessed.
    """
    if not data:
        raise CodecRejectedError("empty Opus packet")
    config = (data[0] >> 3) & 0x1F
    code = data[0] & 0x03
    # RFC 6716 Table 1: per-config frame duration in milliseconds.
    if config < 4:
        frame_ms = (10.0, 20.0, 40.0, 60.0)[config]
    elif config < 8:
        frame_ms = (2.5, 5.0, 10.0, 20.0)[config - 4]
    elif config < 12:
        frame_ms = (10.0, 20.0, 40.0, 60.0)[config - 8]
    else:
        frame_ms = (10.0, 20.0, 40.0, 60.0)[(config - 12) % 4]
    counts = (1, 2, 2)
    if code == 3:
        raise CodecRejectedError(
            "Opus code-3 packets need internal framing and are not accepted")
    frames = counts[code]
    duration = int(round(frame_ms * frames * OPUS_SAMPLE_RATE / 1000.0))
    if duration <= 0 or duration > OPUS_MAX_FRAME_SAMPLES * 2:
        raise CodecRejectedError(f"implausible Opus duration {duration}")
    return duration


# --- dispatch ---------------------------------------------------------------

def map_codec(track: Track) -> CodecMapping:
    if track.type == TRACK_TYPE_SUBTITLE:
        raise CodecRejectedError(
            f"track {track.number}: subtitle tracks ({track.codec_id}) "
            "cannot be carried in MP4 by this remuxer")
    if track.type == TRACK_TYPE_VIDEO:
        if track.codec_id == CODEC_AVC:
            return _map_avc(track)
        if track.codec_id == CODEC_HEVC:
            return _map_hevc(track)
        raise CodecRejectedError(
            f"track {track.number}: unsupported video codec {track.codec_id}")
    if track.type == TRACK_TYPE_AUDIO:
        if track.codec_id == CODEC_AAC:
            return _map_aac(track)
        if track.codec_id == CODEC_OPUS:
            return _map_opus(track)
        raise CodecRejectedError(
            f"track {track.number}: unsupported audio codec {track.codec_id}")
    raise CodecRejectedError(
        f"track {track.number}: track type {track.type} "
        f"({track.codec_id}) has no MP4 mapping")
