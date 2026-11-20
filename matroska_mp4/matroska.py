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
