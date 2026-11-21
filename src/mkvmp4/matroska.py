"""Matroska semantic layer: Segment, Info, Tracks and block extraction.

Built directly on :mod:`mkvmp4.ebml` / :mod:`mkvmp4.tree`; no third-party
container parser is involved.  Media payloads are never copied into
memory during inspection: blocks carry offsets into the input file.
"""

from __future__ import annotations

from dataclasses import dataclass, field

from .ebml import decode_float, decode_int, decode_string, decode_uint
from .errors import EbmlParseError, InputError, UnsupportedError
from .tree import RawNode, parse_tree

# ID constants used by the semantic layer.
ID_EBML = 0x1A45DFA3
ID_DOCTYPE = 0x4282
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
ID_FLAG_LACING = 0x9C
ID_NAME = 0x536E
ID_LANGUAGE = 0x22B59C
ID_AUDIO = 0xE1
ID_SAMPLING_FREQUENCY = 0xB5
ID_CHANNELS = 0x9F
ID_VIDEO = 0xE0
ID_PIXEL_WIDTH = 0xB0
ID_PIXEL_HEIGHT = 0xBA
ID_CLUSTER = 0x1F43B675
ID_CLUSTER_TIMESTAMP = 0xE7
ID_SIMPLE_BLOCK = 0xA3
ID_BLOCK_GROUP = 0xA0
ID_BLOCK = 0xA1
ID_BLOCK_DURATION = 0x9B

TRACK_TYPE_VIDEO = 1
TRACK_TYPE_AUDIO = 2
TRACK_TYPE_SUBTITLE = 17

TRACK_TYPE_NAMES = {
    TRACK_TYPE_VIDEO: "video",
    TRACK_TYPE_AUDIO: "audio",
    TRACK_TYPE_SUBTITLE: "subtitle",
}

DEFAULT_TIMESTAMP_SCALE = 1_000_000  # Matroska default: 1 ms in ns

# Codec IDs this tool maps without transcoding.  Anything else is rejected.
SUPPORTED_CODECS: dict[str, tuple[str, str]] = {
    "V_MPEG4/ISO/AVC": ("video", "avc1"),
    "V_MPEGH/ISO/HEVC": ("video", "hvc1"),
    "V_VP9": ("video", "vp09"),
    "V_VP8": ("video", "vp08"),
    "A_AAC": ("audio", "mp4a"),
    "A_OPUS": ("audio", "Opus"),
    "A_VORBIS": ("audio", "vorbis"),
    "S_TEXT/UTF8": ("subtitle", "stpp"),
    "S_TEXT/WEBVTT": ("subtitle", "wvtt"),
}


@dataclass
class Track:
    number: int
    uid: int | None
    kind: int
    track_type: str
    codec_id: str
    codec_private: bytes
    default_duration_ns: int | None
    flag_lacing: bool
    name: str | None
    language: str | None
    pixel_width: int | None = None
    pixel_height: int | None = None
    sampling_frequency: float | None = None
    channels: int | None = None
    entry_offset: int = 0
    path: str = ""

    @property
    def codec_supported(self) -> bool:
        return self.codec_id in SUPPORTED_CODECS

    @property
    def mp4_sample_entry(self) -> str | None:
        entry = SUPPORTED_CODECS.get(self.codec_id)
        return entry[1] if entry else None

    def to_dict(self) -> dict[str, object]:
        return {
            "number": self.number,
            "uid": self.uid,
            "type": self.track_type,
            "codec_id": self.codec_id,
            "codec_supported": self.codec_supported,
            "mp4_sample_entry": self.mp4_sample_entry,
            "default_duration_ns": self.default_duration_ns,
            "flag_lacing": self.flag_lacing,
            "name": self.name,
            "language": self.language,
            "pixel_width": self.pixel_width,
            "pixel_height": self.pixel_height,
            "sampling_frequency": self.sampling_frequency,
            "channels": self.channels,
            "entry_offset": self.entry_offset,
            "path": self.path,
        }


@dataclass
class Block:
    """One frame reference: payload stays in the caller's buffer."""

    track: int
    data_offset: int
    data_length: int
    timecode: int  # signed relative timecode from the block header
    flags: int
    cluster_offset: int
    lace_index: int = 0
    lace_count: int = 1
    block_duration: int | None = None  # BlockDuration in TimestampScale ticks
    sample_index: int = -1  # filled in decode order by the pipeline

    @property
    def keyframe(self) -> bool:
        return bool(self.flags & 0x80)

    @property
    def lacing(self) -> int:
        return (self.flags >> 1) & 0x03

    @property
    def discardable(self) -> bool:
        return bool(self.flags & 0x01)


@dataclass
class Cluster:
    offset: int
    end_offset: int
    timestamp: int
    blocks: list[Block] = field(default_factory=list)
    unknown_size: bool = False


@dataclass
class MatroskaFile:
    data: bytes
    path: str
    doc_type: str
    timestamp_scale: int
    segment_offset: int
    segment_data_offset: int
    segment_end: int
    segment_unknown_size: bool
    tracks: list[Track]
    clusters: list[Cluster]
    muxing_app: str | None = None
    writing_app: str | None = None
    tree_stats: dict[str, object] = field(default_factory=dict)

    @property
    def timestamp_scale_ns(self) -> int:
        return self.timestamp_scale


def _uint_field(data: bytes, node: RawNode, base: int) -> int:
    return decode_uint(data, node.data_offset, node.size, base=base, path=node.path)


def _int_field(data: bytes, node: RawNode, base: int) -> int:
    return decode_int(data, node.data_offset, node.size, base=base, path=node.path)


def _find_child(nodes: list[RawNode], element_id: int) -> RawNode | None:
    for node in nodes:
        if node.element_id == element_id:
            return node
    return None

