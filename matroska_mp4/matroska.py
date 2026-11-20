"""Matroska track/block parsing on top of the bounded EBML layer."""

from __future__ import annotations

import array
import math
import os
from dataclasses import dataclass
from typing import BinaryIO

import numpy as np

from . import ebml
from . import ebml_ids as ids

MAX_PACKET_BYTES = 256 * 1024 * 1024
MAX_SAMPLES = 5_000_000
# Timestamps are kept in signed 64-bit arrays; keep a safety margin for the
# PTS arithmetic that follows (track offsets and sample durations).
MAX_TIMESTAMP_NS = 1 << 62
MAX_DURATION_NS = (1 << 63) - 1
# A lace count byte encodes 1..256 frames; the guard only bounds pathological
# payloads (256 frames of 256 MiB cannot be a valid single Block).
MAX_LACE_FRAMES = 256
# Blocks are retained as objects only for small inputs; larger inputs keep just
# the compact per-track index (offsets/sizes/PTS/durations/keyframes).
BLOCK_RETENTION_BYTES = 64 * 1024 * 1024

VIDEO_CODEC = "V_MPEG4/ISO/AVC"
AAC_PREFIX = "A_AAC"
# Sample entries store these as 16-bit fields in ISO-BMFF.
MAX_TRACK_DIMENSION = 0xFFFF
MAX_AUDIO_CHANNELS = 0xFFFF
MAX_AUDIO_RATE = 0xFFFF


class UnsupportedFormat(Exception):
    """A track/attachment/codec we cannot represent in MP4."""

    def __init__(self, message: str, *, track: int | None = None):
        self.track = track
        self.message = message
        super().__init__(message)


# Resource-limit failures live with the EBML layer; re-export for callers.
LimitError = ebml.LimitError


@dataclass(slots=True)
class FrameRef:
    offset: int
    size: int


@dataclass(slots=True)
class Block:
    track_number: int
    cluster_index: int
    cluster_timecode: int
    relative_timecode: int
    flags: int
    lacing: str
    frames: list[FrameRef]
    offset: int
    is_simple: bool
    keyframe: bool
    duration_ticks: int | None = None
    reference_blocks: int = 0
    discard_padding: int | None = None

    @property
    def timecode(self) -> int:
        return self.cluster_timecode + self.relative_timecode


@dataclass(slots=True)
class Track:
    number: int
    uid: int | None
    track_type: int
    codec_id: str
    codec_private: bytes
    default_duration_ns: int | None
    name: str | None
    language: str | None
    width: int | None
    height: int | None
    sample_rate: float | None
    channels: int | None
    mp4_codec: str
    handler: str


@dataclass(slots=True)
class TrackSamples:
    """Compact per-track sample index (all NumPy int64/bool arrays)."""

    pts_ns: np.ndarray
    offset: np.ndarray
    size: np.ndarray
    duration_ns: np.ndarray
    keyframe: np.ndarray


@dataclass(slots=True)
class ParsedMatroska:
    path: str
    document: ebml.Document
    timestamp_scale_ns: int
    tracks: list[Track]
    samples: dict[int, TrackSamples]
    duration_ticks: float | None
    attachment_count: int
    frame_count: int
    media_bytes: int
    clusters: int
    simple_blocks: int
    block_groups: int
    lacing_counts: dict[str, int]
    blocks: list[Block] | None = None

    def track_by_number(self, number: int) -> Track:
        for track in self.tracks:
            if track.number == number:
                return track
        raise KeyError(number)


class _PayloadReader:
    """Streaming reader confined to one Block payload (never media-sized)."""

    def __init__(self, fh: BinaryIO, start: int, end: int):
        self.fh = fh
        self.pos = start
        self.end = end
        self._buf = b""
        self._buf_pos = 0

    def _fill(self) -> bool:
        if self.pos >= self.end:
            return False
        take = min(4096, self.end - self.pos)
        self.fh.seek(self.pos)
        self._buf = self.fh.read(take)
        self._buf_pos = 0
        self.pos += len(self._buf)
        return bool(self._buf)

    def read_byte(self) -> int:
        if self._buf_pos >= len(self._buf):
            if not self._fill():
                raise ebml.EBMLError(self.pos, "truncated block payload")
        value = self._buf[self._buf_pos]
        self._buf_pos += 1
        return value

    def read(self, count: int) -> bytes:
        if count < 0:
            raise ebml.EBMLError(self.pos, "negative block read")
        out = bytearray()
        while len(out) < count:
            if self._buf_pos >= len(self._buf):
                if not self._fill():
                    raise ebml.EBMLError(self.pos, "truncated block payload")
            take = min(count - len(out), len(self._buf) - self._buf_pos)
            out += self._buf[self._buf_pos:self._buf_pos + take]
            self._buf_pos += take
        return bytes(out)

    @property
    def remaining(self) -> int:
        return self.end - (self.pos - (len(self._buf) - self._buf_pos))

    def read_vint(self, *, signed: bool) -> int:
        first = self.read_byte()
        if first == 0:
            raise ebml.EBMLError(self.pos - 1, "invalid block VINT: zero lead byte")
        length = 1
        mask = 0x80
        while not (first & mask):
            mask >>= 1
            length += 1
        if length > 8:
            raise ebml.EBMLError(self.pos - 1, "block VINT longer than 8 bytes")
        rest = self.read(length - 1)
        raw = int.from_bytes(bytes([first]) + rest, "big")
        value = raw & ((1 << (7 * length)) - 1)
        if signed:
            value -= (1 << (7 * length - 1)) - 1
        return value


def _leaf(fh: BinaryIO, parent: ebml.Element, element_id: int) -> bytes | None:
    child = parent.find(element_id)
    return None if child is None else ebml.read_leaf(fh, child)


def _uint_leaf(fh: BinaryIO, parent: ebml.Element, element_id: int) -> int | None:
    data = _leaf(fh, parent, element_id)
    return None if data is None else ebml.uint_value(data)


def _text_leaf(fh: BinaryIO, parent: ebml.Element, element_id: int) -> str | None:
    data = _leaf(fh, parent, element_id)
    if data is None:
        return None
    return data.decode("utf-8", "replace").rstrip("\x00")


def _float_leaf(fh: BinaryIO, parent: ebml.Element, element_id: int) -> float | None:
    data = _leaf(fh, parent, element_id)
    if data is None:
        return None
    try:
        return ebml.float_value(data)
    except ValueError as exc:
        raise ebml.EBMLError(parent.offset, str(exc)) from exc


def validate_avcc(data: bytes) -> None:
    if len(data) < 7 or data[0] != 1:
        raise UnsupportedFormat("invalid H.264 CodecPrivate (avcC)")
    length_size = (data[4] & 0x03) + 1
    if length_size == 3:
        raise UnsupportedFormat("invalid avcC NAL length size 3")
    pos = 6
    num_sps = data[5] & 0x1F
    if num_sps == 0:
        raise UnsupportedFormat("avcC has no SPS")
    for _ in range(num_sps):
        if pos + 2 > len(data):
            raise UnsupportedFormat("truncated avcC SPS length")
        size = int.from_bytes(data[pos:pos + 2], "big")
        pos += 2
        if size == 0 or pos + size > len(data):
            raise UnsupportedFormat("truncated avcC SPS payload")
        pos += size
    if pos >= len(data):
        raise UnsupportedFormat("avcC has no PPS count")
    num_pps = data[pos]
    pos += 1
    if num_pps == 0:
        raise UnsupportedFormat("avcC has no PPS")
    for _ in range(num_pps):
        if pos + 2 > len(data):
            raise UnsupportedFormat("truncated avcC PPS length")
        size = int.from_bytes(data[pos:pos + 2], "big")
        pos += 2
        if size == 0 or pos + size > len(data):
            raise UnsupportedFormat("truncated avcC PPS payload")
        pos += size


def validate_audio_specific_config(data: bytes) -> None:
    if len(data) < 2:
        raise UnsupportedFormat("invalid AAC CodecPrivate (AudioSpecificConfig)")
    bit_pos = 0

    def bits(count: int) -> int:
        nonlocal bit_pos
        if bit_pos + count > len(data) * 8:
            raise UnsupportedFormat("truncated AudioSpecificConfig")
        value = 0
        for _ in range(count):
            byte = data[bit_pos // 8]
            value = (value << 1) | ((byte >> (7 - (bit_pos % 8))) & 1)
            bit_pos += 1
        return value

    object_type = bits(5)
    if object_type == 31:
        object_type = 32 + bits(6)
    if object_type == 0:
        raise UnsupportedFormat("AAC object type 0 is invalid")
    freq_index = bits(4)
    if freq_index == 15:
        bits(24)
    channels = bits(4)
    if channels == 0:
        raise UnsupportedFormat("AAC channel configuration 0 is unsupported")
    if object_type in (5, 29):
        extension_freq = bits(4)
        if extension_freq == 15:
            bits(24)
        bits(5)


def _read_track(fh: BinaryIO, entry: ebml.Element) -> Track:
    number = _uint_leaf(fh, entry, ids.TRACK_NUMBER)
    if not number:
        raise UnsupportedFormat("TrackEntry without TrackNumber")
    track_type = _uint_leaf(fh, entry, ids.TRACK_TYPE)
    codec_id = _text_leaf(fh, entry, ids.CODEC_ID)
    if not codec_id:
        raise UnsupportedFormat("TrackEntry without CodecID", track=number)
    codec_private = _leaf(fh, entry, ids.CODEC_PRIVATE) or b""
    uid = _uint_leaf(fh, entry, ids.TRACK_UID)
    default_duration = _uint_leaf(fh, entry, ids.DEFAULT_DURATION)
    if default_duration is not None:
        if default_duration <= 0:
            default_duration = None
        elif default_duration > MAX_DURATION_NS:
            raise LimitError("DefaultDuration overflows int64",
                             offset=entry.offset)
    name = _text_leaf(fh, entry, ids.NAME)
    language = _text_leaf(fh, entry, ids.LANGUAGE)
    if entry.find(ids.CONTENT_ENCODINGS) is not None:
        # Compressed or encrypted frames would need to be decoded before the
        # bytes could be copied into MP4.
        raise UnsupportedFormat("ContentEncodings are unsupported", track=number)
    track_scale = _float_leaf(fh, entry, ids.TRACK_TIMESTAMP_SCALE)
    if track_scale is not None and track_scale != 1.0:
        raise UnsupportedFormat(
            f"TrackTimestampScale {track_scale:g} is unsupported", track=number)
    codec_delay = _uint_leaf(fh, entry, ids.CODE_DELAY)
    if codec_delay:
        raise UnsupportedFormat(
            f"CodecDelay {codec_delay} ns is unsupported", track=number)
    if _uint_leaf(fh, entry, ids.MAX_BLOCK_ADDITION_ID):
        # BlockAdditions carry extra per-block data we do not copy.
        raise UnsupportedFormat("BlockAdditions are unsupported", track=number)
    width = height = channels = None
    sample_rate = None
    if track_type == 1:
        if codec_id != VIDEO_CODEC:
            raise UnsupportedFormat(f"unsupported video codec {codec_id}", track=number)
        if not codec_private:
            raise UnsupportedFormat("H.264 track lacks CodecPrivate", track=number)
        validate_avcc(codec_private)
        video = entry.find(ids.VIDEO)
        if video is None:
            raise UnsupportedFormat("H.264 track lacks Video settings", track=number)
        width = _uint_leaf(fh, video, ids.PIXEL_WIDTH)
        height = _uint_leaf(fh, video, ids.PIXEL_HEIGHT)
        if not width or not height:
            raise UnsupportedFormat("H.264 track lacks pixel dimensions", track=number)
        if width > MAX_TRACK_DIMENSION or height > MAX_TRACK_DIMENSION:
            raise UnsupportedFormat(
                f"video dimensions {width}x{height} exceed MP4 16-bit sample "
                f"entry range", track=number)
        mp4_codec, handler = "avc1", "vide"
    elif track_type == 2:
        if not codec_id.startswith(AAC_PREFIX):
            raise UnsupportedFormat(f"unsupported audio codec {codec_id}", track=number)
        if not codec_private:
            raise UnsupportedFormat("AAC track lacks CodecPrivate", track=number)
        validate_audio_specific_config(codec_private)
        audio = entry.find(ids.AUDIO)
        if audio is None:
            raise UnsupportedFormat("AAC track lacks Audio settings", track=number)
        sample_rate = _float_leaf(fh, audio, ids.SAMPLING_FREQUENCY)
        channels = _uint_leaf(fh, audio, ids.CHANNELS)
        if not sample_rate or sample_rate <= 0 or not channels:
            raise UnsupportedFormat("AAC track lacks sample rate/channels", track=number)
        if channels > MAX_AUDIO_CHANNELS:
            raise UnsupportedFormat(
                f"audio channel count {channels} exceeds MP4 16-bit range",
                track=number)
        if not math.isfinite(sample_rate):
            raise UnsupportedFormat("AAC sample rate is not finite", track=number)
        if sample_rate > MAX_AUDIO_RATE:
            raise UnsupportedFormat(
                f"AAC sample rate {sample_rate:g} Hz exceeds the MP4 16.16 "
                f"sample entry range", track=number)
        mp4_codec, handler = "mp4a", "soun"
    else:
        kind = {17: "subtitle", 18: "button", 32: "control"}.get(track_type, "unknown")
        raise UnsupportedFormat(f"unsupported {kind} track type {track_type}",
                                track=number)
    return Track(number=number, uid=uid, track_type=track_type, codec_id=codec_id,
                 codec_private=codec_private, default_duration_ns=default_duration,
                 name=name, language=language, width=width, height=height,
                 sample_rate=sample_rate, channels=channels, mp4_codec=mp4_codec,
                 handler=handler)


def _parse_lace_sizes(reader: _PayloadReader, lacing: int) -> list[int]:
    """Return per-frame sizes for a block's lacing mode.

    ``lacing`` is 0 none, 1 Xiph, 2 fixed, 3 EBML.  The payload header is
    bounded by the block size, so a truncated lace raises with an offset.
    """

    if lacing == 0:
        size = reader.remaining
        if size <= 0:
            raise ebml.EBMLError(reader.pos, "empty Block payload")
        return [size]
    count = reader.read_byte() + 1
    if count == 1:
        # No lace size headers are stored when only one frame is present,
        # regardless of the lacing mode declared in the flags.
        size = reader.remaining
        if size <= 0:
            raise ebml.EBMLError(reader.pos, "empty laced block payload")
        return [size]
    sizes: list[int] = []
    if lacing == 1:  # Xiph: sum of 255-terminated values per frame.
        for _ in range(count - 1):
            size = 0
            while True:
                part = reader.read_byte()
                size += part
                if part != 255:
                    break
            sizes.append(size)
    elif lacing == 2:  # fixed: divide the remainder evenly.
        remaining = reader.remaining
        if remaining % count:
            raise ebml.EBMLError(reader.pos, "fixed lacing size is not divisible")
        return [remaining // count] * count
    elif lacing == 3:  # EBML: first size then signed deltas.
        sizes.append(reader.read_vint(signed=False))
        for _ in range(count - 2):
            next_size = sizes[-1] + reader.read_vint(signed=True)
            if next_size < 0:
                raise ebml.EBMLError(reader.pos, "EBML lace size became negative")
            sizes.append(next_size)
    else:
        raise ebml.EBMLError(reader.pos, f"invalid lacing value {lacing}")
    remaining = reader.remaining
    if sum(sizes) > remaining:
        raise ebml.EBMLError(reader.pos, "lace sizes exceed block payload")
    sizes.append(remaining - sum(sizes))
    if len(sizes) != count:
        raise ebml.EBMLError(reader.pos, f"lacing describes {len(sizes)} frames "
                                         f"but declared {count}")
    return sizes


def _parse_block(fh: BinaryIO, element: ebml.Element, cluster_index: int,
                 cluster_timecode: int, is_simple: bool, duration: int | None,
                 references: int, discard_padding: int | None = None) -> Block:
    if element.size is None or element.size < 4:
        raise ebml.EBMLError(element.offset, "Block payload too small")
    if element.size > MAX_PACKET_BYTES * MAX_LACE_FRAMES:
        raise LimitError("Block payload exceeds lacing safety bound",
                         offset=element.offset)
    end = element.data_offset + element.size
    reader = _PayloadReader(fh, element.data_offset, end)
    first = reader.read_byte()
    if first == 0:
        raise ebml.EBMLError(element.data_offset,
                             "invalid block track number VINT: leading zero byte")
    length = 1
    mask = 0x80
    while not (first & mask):
        mask >>= 1
        length += 1
    if length > 8:
        raise ebml.EBMLError(element.data_offset, "invalid track number VINT")
    rest = reader.read(length - 1)
    track = int.from_bytes(bytes([first]) + rest, "big") & ((1 << (7 * length)) - 1)
    if track == 0:
        raise ebml.EBMLError(element.data_offset, "block track number 0")
    relative = int.from_bytes(reader.read(2), "big", signed=True)
    flags = reader.read_byte()
    lacing = (flags >> 1) & 0x03
    sizes = _parse_lace_sizes(reader, lacing)
    offset = reader.pos - (len(reader._buf) - reader._buf_pos)
    frames: list[FrameRef] = []
    for size in sizes:
        if size <= 0:
            raise ebml.EBMLError(offset, "empty frame in lace")
        if size > MAX_PACKET_BYTES:
            raise LimitError(f"frame is {size} bytes; limit is {MAX_PACKET_BYTES}",
                             offset=offset)
        frames.append(FrameRef(offset=offset, size=size))
        offset += size
    if offset != end:
        raise ebml.EBMLError(offset, "block frame sizes overrun payload")
    keyframe = bool(flags & 0x80) if is_simple else references == 0
    return Block(track_number=track, cluster_index=cluster_index,
                 cluster_timecode=cluster_timecode, relative_timecode=relative,
                 flags=flags, lacing=("none", "xiph", "fixed", "ebml")[lacing],
                 frames=frames, offset=element.offset, is_simple=is_simple,
                 keyframe=keyframe, duration_ticks=duration,
                 reference_blocks=references, discard_padding=discard_padding)


class _SampleCollector:
    """Streaming hook: parses blocks into compact per-track arrays."""

    def __init__(self, path: str, scale_ns: int, known_tracks: set[int],
                 keep_blocks: bool):
        self.path = path
        self.scale_ns = scale_ns
        self.known_tracks = known_tracks
        self.keep_blocks = keep_blocks
        self.blocks: list[Block] | None = [] if self.keep_blocks else None
        self.arrays = {
            number: {"pts": array.array("q"), "offset": array.array("q"),
                     "size": array.array("q"), "duration": array.array("q"),
                     "key": bytearray()} for number in known_tracks}
        self.cluster_ids: dict[int, tuple[int, int]] = {}
        self.track_defaults: dict[int, int | None] = {}
        self.frame_count = 0
        self.media_bytes = 0
        self.simple_blocks = 0
        self.block_groups = 0
        self.lacing_counts = {"none": 0, "xiph": 0, "fixed": 0, "ebml": 0}
        self.cluster_count = 0

    def _cluster_info(self, fh: BinaryIO, cluster: ebml.Element) -> tuple[int, int]:
        cached = self.cluster_ids.get(cluster.offset)
        if cached is not None:
            return cached
        timecode = _uint_leaf(fh, cluster, ids.TIMECODE)
        if timecode is None:
            raise ebml.EBMLError(cluster.offset, "Cluster lacks Timecode")
        info = (self.cluster_count, timecode)
        self.cluster_ids[cluster.offset] = info
        self.cluster_count += 1
        return info

    def __call__(self, fh: BinaryIO, parent: ebml.Element,
                 child: ebml.Element) -> None:
        cluster_index, cluster_timecode = self._cluster_info(fh, parent)
        if child.element_id == ids.SIMPLE_BLOCK:
            block = _parse_block(fh, child, cluster_index, cluster_timecode,
                                 True, None, 0)
            self.simple_blocks += 1
        elif child.element_id == ids.BLOCK_GROUP:
            self.block_groups += 1
            block_elem = child.find(ids.BLOCK)
            if block_elem is None:
                raise ebml.EBMLError(child.offset, "BlockGroup lacks Block")
            duration = _uint_leaf(fh, child, ids.BLOCK_DURATION)
            if duration is not None and duration <= 0:
                duration = None
            refs = len(child.children_of(ids.REFERENCE_BLOCK))
            padding_data = _leaf(fh, child, ids.DISCARD_PADDING)
            padding = None if padding_data is None else ebml.int_value(padding_data)
            block = _parse_block(fh, block_elem, cluster_index, cluster_timecode,
                                 False, duration, refs, padding)
            if padding:
                # MP4 would need sample groups to trim the frame; silently
                # copying the padding would shift audio, so fail instead.
                raise UnsupportedFormat(
                    f"DiscardPadding {padding} ns is unsupported",
                    track=block.track_number)
        else:
            return
        if block.track_number not in self.known_tracks:
            raise ebml.EBMLError(block.offset,
                                 f"block references unknown track {block.track_number}")
        self._record(block)
        if self.blocks is not None:
            self.blocks.append(block)

    def _record(self, block: Block) -> None:
        bucket = self.arrays[block.track_number]
        count = len(block.frames)
        scale = self.scale_ns
        if abs(block.timecode) > MAX_TIMESTAMP_NS // scale:
            raise LimitError("block PTS overflows int64", offset=block.offset)
        block_pts = block.timecode * scale
        block_dur = None
        if block.duration_ticks is not None:
            if block.duration_ticks > MAX_DURATION_NS // scale:
                raise LimitError("BlockDuration overflows int64",
                                 offset=block.offset)
            block_dur = block.duration_ticks * scale
        per_frame = None
        if count > 1:
            if block_dur is not None and block_dur % count == 0:
                per_frame = block_dur // count
            else:
                default = self.track_defaults.get(block.track_number)
                if default:
                    per_frame = default
                else:
                    raise ebml.EBMLError(
                        block.offset,
                        f"laced block has {count} frames but no derivable "
                        f"per-frame duration")
        self.lacing_counts[block.lacing] += 1
        last_pts = block_pts
        if per_frame is not None and count > 1:
            last_pts = block_pts + per_frame * (count - 1)
        if abs(last_pts) > MAX_TIMESTAMP_NS:
            raise LimitError("laced frame PTS overflows int64", offset=block.offset)
        for index, frame in enumerate(block.frames):
            pts = block_pts + (per_frame * index if per_frame is not None else 0)
            bucket["pts"].append(pts)
            bucket["offset"].append(frame.offset)
            bucket["size"].append(frame.size)
            duration = per_frame if count > 1 else block_dur
            bucket["duration"].append(duration or 0)
            bucket["key"].append(1 if block.keyframe else 0)
        self.frame_count += count
        if self.frame_count > MAX_SAMPLES:
            raise LimitError(f"sample count exceeds {MAX_SAMPLES}",
                             offset=block.offset)
        self.media_bytes += sum(f.size for f in block.frames)
        if self.media_bytes > ebml.MAX_INPUT_BYTES:
            raise LimitError("frame payload exceeds input byte limit",
                             offset=block.offset)

    def finish(self) -> dict[int, TrackSamples]:
        result = {}
        for number, bucket in self.arrays.items():
            result[number] = TrackSamples(
                pts_ns=_to_int64(bucket["pts"]),
                offset=_to_int64(bucket["offset"]),
                size=_to_int64(bucket["size"]),
                duration_ns=_to_int64(bucket["duration"]),
                keyframe=np.frombuffer(bytes(bucket["key"]), dtype=np.uint8)
                .astype(bool))
        return result


def _to_int64(values: array.array) -> np.ndarray:
    """Zero-copy conversion of an array.array('q') to a read-only int64 array."""

    return np.frombuffer(values, dtype=np.int64)


def parse_matroska(path: str, *, keep_blocks: bool | None = None) -> ParsedMatroska:
    """Parse tracks and sample data; media bytes are never buffered."""

    holder: dict[str, object] = {}
    file_size = os.path.getsize(path) if os.path.isfile(path) else 0
    holder["keep_blocks"] = (file_size <= BLOCK_RETENTION_BYTES
                             if keep_blocks is None else keep_blocks)

    def hook(fh: BinaryIO, parent: ebml.Element, child: ebml.Element) -> None:
        collector = holder.get("collector")
        if collector is None:
            collector = _SampleCollector(path, holder["scale"], holder["tracks"],
                                         holder["keep_blocks"])
            collector.track_defaults = holder["defaults"]
            holder["collector"] = collector
        collector(fh, parent, child)

    # Two-pass: a lightweight metadata pass determines tracks and TimestampScale
    # before blocks are streamed into the collector.
    discard = lambda fh, parent, child: None  # noqa: E731 - metadata only
    document = ebml.parse_file(path, presentation_hook=discard)
    segment = document.find_root(ids.SEGMENT)
    if segment is None:
        raise ebml.EBMLError(0, "missing Segment")
    attachments = 0
    attach = segment.find(ids.ATTACHMENTS)
    if attach is not None:
        files = attach.children_of(ids.ATTACHED_FILE)
        attachments = len(files)
        if attachments:
            name = files[0].find(ids.FILE_NAME)
            label = ""
            if name is not None:
                with open(path, "rb") as fh:
                    label = " " + (ebml.read_leaf(fh, name).decode("utf-8", "replace"))
            raise UnsupportedFormat(f"attachments are unsupported (found{label})")
    info = segment.find(ids.INFO)
    timestamp_scale = 1_000_000
    duration_ticks: float | None = None
    with open(path, "rb") as fh:
        if info is not None:
            value = _uint_leaf(fh, info, ids.TIMESTAMP_SCALE)
            if value is not None:
                timestamp_scale = value
            duration_ticks = _float_leaf(fh, info, ids.DURATION)
        if timestamp_scale <= 0:
            raise ebml.EBMLError(info.offset if info else 0,
                                 "TimestampScale must be positive")
        tracks_elem = segment.find(ids.TRACKS)
        if tracks_elem is None:
            raise ebml.EBMLError(segment.offset, "missing Tracks")
        tracks = [_read_track(fh, entry)
                  for entry in tracks_elem.children_of(ids.TRACK_ENTRY)]
    if not tracks:
        raise ebml.EBMLError(segment.offset, "Tracks has no TrackEntry")
    numbers = [t.number for t in tracks]
    if len(set(numbers)) != len(numbers):
        raise ebml.EBMLError(segment.offset, "duplicate TrackNumber")
    tracks.sort(key=lambda t: t.number)
    holder["scale"] = timestamp_scale
    holder["tracks"] = set(numbers)
    holder["defaults"] = {t.number: t.default_duration_ns for t in tracks}
    ebml.parse_file(path, presentation_hook=hook)
    collector = holder.get("collector")
    if collector is None:
        raise ebml.EBMLError(segment.offset, "no blocks found")
    if collector.frame_count == 0:
        raise ebml.EBMLError(segment.offset, "no blocks found")
    return ParsedMatroska(
        path=path, document=document, timestamp_scale_ns=timestamp_scale,
        tracks=tracks, samples=collector.finish(),
        duration_ticks=duration_ticks, attachment_count=attachments,
        frame_count=collector.frame_count, media_bytes=collector.media_bytes,
        clusters=document.counts().get("Cluster", 0),
        simple_blocks=collector.simple_blocks, block_groups=collector.block_groups,
        lacing_counts=collector.lacing_counts, blocks=collector.blocks)
