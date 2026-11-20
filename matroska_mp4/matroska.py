"""Matroska track/block parsing on top of the bounded EBML layer."""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from typing import BinaryIO

from . import ebml
from . import ebml_ids as ids

MAX_PACKET_BYTES = 256 * 1024 * 1024
MAX_SAMPLES = 5_000_000
MAX_LACE_FRAMES = 255

VIDEO_CODEC = "V_MPEG4/ISO/AVC"
AAC_PREFIX = "A_AAC"


class UnsupportedFormat(Exception):
    """A track/attachment/codec we cannot represent in MP4."""

    def __init__(self, message: str, *, track: int | None = None):
        self.track = track
        self.message = message
        super().__init__(message)


class LimitError(Exception):
    def __init__(self, message: str, *, offset: int | None = None):
        self.offset = offset
        self.message = message
        super().__init__(message)


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
class ParsedMatroska:
    path: str
    document: ebml.Document
    timestamp_scale_ns: int
    tracks: list[Track]
    blocks: list[Block]
    duration_ticks: float | None
    attachment_count: int
    frame_count: int
    media_bytes: int
    clusters: int
    simple_blocks: int
    block_groups: int

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
    return None if data is None else ebml.float_value(data)


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
    if default_duration is not None and default_duration <= 0:
        default_duration = None
    name = _text_leaf(fh, entry, ids.NAME)
    language = _text_leaf(fh, entry, ids.LANGUAGE)
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
    if lacing == 0:
        size = reader.remaining
        if size <= 0:
            raise ebml.EBMLError(reader.pos, "empty Block payload")
        return [size]
    count = reader.read_byte() + 1
    if count < 1 or count > MAX_LACE_FRAMES:
        raise ebml.EBMLError(reader.pos - 1, f"invalid lace frame count {count}")
    sizes: list[int] = []
    if lacing == 1:  # Xiph
        for _ in range(count - 1):
            size = 0
            while True:
                part = reader.read_byte()
                size += part
                if part != 255:
                    break
            sizes.append(size)
    elif lacing == 2:  # fixed
        remaining = reader.remaining
        if remaining % count:
            raise ebml.EBMLError(reader.pos, "fixed lacing size is not divisible")
        sizes = [remaining // count] * count
    elif lacing == 3:  # EBML
        first = reader.read_vint(signed=False)
        sizes.append(first)
        for _ in range(count - 2):
            delta = reader.read_vint(signed=True)
            next_size = sizes[-1] + delta
            if next_size < 0:
                raise ebml.EBMLError(reader.pos, "EBML lace size became negative")
            sizes.append(next_size)
    else:
        raise ebml.EBMLError(reader.pos, f"invalid lacing value {lacing}")
    used = sum(sizes)
    remaining = reader.remaining
    last = remaining - used
    if lacing in (1, 3):
        if last < 0:
            raise ebml.EBMLError(reader.pos, "lace sizes exceed block payload")
        sizes.append(last)
    elif lacing == 2:
        pass
    total = sum(sizes)
    if total != remaining:
        raise ebml.EBMLError(reader.pos, "lace sizes do not sum to payload size")
    return sizes


def _parse_block(fh: BinaryIO, element: ebml.Element, cluster_index: int,
                 cluster_timecode: int, is_simple: bool, duration: int | None,
                 references: int) -> Block:
    if element.size is None or element.size < 4:
        raise ebml.EBMLError(element.offset, "Block payload too small")
    if element.size > MAX_PACKET_BYTES * MAX_LACE_FRAMES:
        raise LimitError("Block payload exceeds lacing safety bound",
                         offset=element.offset)
    end = element.data_offset + element.size
    reader = _PayloadReader(fh, element.data_offset, end)
    first = reader.read_byte()
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
                 reference_blocks=references, discard_padding=None)


def parse_matroska(path: str) -> ParsedMatroska:
    document = ebml.parse_file(path)
    file_size = document.file_size
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
            raise ebml.EBMLError(tracks_elem.offset, "Tracks has no TrackEntry")
        numbers = [t.number for t in tracks]
        if len(set(numbers)) != len(numbers):
            raise ebml.EBMLError(tracks_elem.offset, "duplicate TrackNumber")
        tracks.sort(key=lambda t: t.number)
        known = set(numbers)
        blocks: list[Block] = []
        frame_count = 0
        media_bytes = 0
        clusters = 0
        simple_count = 0
        blockgroup_count = 0
        for cluster_index, cluster in enumerate(segment.children_of(ids.CLUSTER)):
            clusters += 1
            timecode = _uint_leaf(fh, cluster, ids.TIMECODE)
            if timecode is None:
                raise ebml.EBMLError(cluster.offset, "Cluster lacks Timecode")
            for child in cluster.children:
                if child.element_id == ids.SIMPLE_BLOCK:
                    block = _parse_block(fh, child, cluster_index, timecode,
                                         True, None, 0)
                    simple_count += 1
                elif child.element_id == ids.BLOCK_GROUP:
                    blockgroup_count += 1
                    block_elem = child.find(ids.BLOCK)
                    if block_elem is None:
                        raise ebml.EBMLError(child.offset, "BlockGroup lacks Block")
                    duration = _uint_leaf(fh, child, ids.BLOCK_DURATION)
                    if duration is not None and duration <= 0:
                        duration = None
                    refs = len(child.children_of(ids.REFERENCE_BLOCK))
                    block = _parse_block(fh, block_elem, cluster_index, timecode,
                                         False, duration, refs)
                    if block.duration_ticks is not None:
                        pass
                else:
                    continue
                if block.track_number not in known:
                    raise ebml.EBMLError(block.offset,
                                         f"block references unknown track {block.track_number}")
                blocks.append(block)
                frame_count += len(block.frames)
                if frame_count > MAX_SAMPLES:
                    raise LimitError(f"sample count exceeds {MAX_SAMPLES}",
                                     offset=block.offset)
                media_bytes += sum(f.size for f in block.frames)
                if media_bytes > ebml.MAX_INPUT_BYTES:
                    raise LimitError("frame payload exceeds input byte limit",
                                     offset=block.offset)
    if not blocks:
        raise ebml.EBMLError(segment.offset, "no blocks found")
    return ParsedMatroska(path=path, document=document,
                          timestamp_scale_ns=timestamp_scale, tracks=tracks,
                          blocks=blocks, duration_ticks=duration_ticks,
                          attachment_count=attachments, frame_count=frame_count,
                          media_bytes=media_bytes, clusters=clusters,
                          simple_blocks=simple_count, block_groups=blockgroup_count)
