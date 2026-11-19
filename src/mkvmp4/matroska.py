"""Matroska (EBML) parsing: IDs, block parsing and the Segment model."""

from __future__ import annotations

from dataclasses import dataclass, field

from .ebml import EbmlError, EbmlReader, Element

# --------------------------------------------------------------------------
# EBML / Matroska element IDs (constants already include VINT markers).
# --------------------------------------------------------------------------

ID_EBML_HEADER = 0x1A45DFA3
ID_EBML_DOC_TYPE = 0x4282
ID_EBML_DOC_TYPE_VERSION = 0x4287
ID_EBML_DOC_TYPE_READ_VERSION = 0x4285

ID_SEGMENT = 0x18538067
ID_SEGMENT_INFO = 0x1549A966
ID_TIMECODE_SCALE = 0x2AD7B1
ID_DURATION = 0x4489
ID_MUXING_APP = 0x4D80
ID_WRITING_APP = 0x5741

ID_TRACKS = 0x1654AE6B
ID_TRACK_ENTRY = 0xAE
ID_TRACK_NUMBER = 0xD7
ID_TRACK_UID = 0x73C5
ID_TRACK_TYPE = 0x83
ID_FLAG_ENABLED = 0xB9
ID_CODEC_ID = 0x86
ID_CODEC_PRIVATE = 0x63A2
ID_DEFAULT_DURATION = 0x23E383
ID_AUDIO = 0xE1
ID_VIDEO = 0xE0
ID_PIXEL_WIDTH = 0xB0
ID_PIXEL_HEIGHT = 0xBA
ID_FRAME_RATE = 0x2383E3
ID_SAMPLING_FREQUENCY = 0xB5
ID_CHANNELS = 0x9F
ID_BIT_DEPTH = 0x6264
ID_CONTENT_ENCODINGS = 0x6D80
ID_CONTENT_ENCODING = 0x6240
ID_CONTENT_COMPRESSION = 0x5034
ID_CONTENT_ENCODING_TYPE = 0x5031
ID_CONTENT_COMP_ALGO = 0x4254
ID_CONTENT_ENC_ALGO = 0x47E1

ID_CLUSTER = 0x1F43B675
ID_CLUSTER_TIMECODE = 0xE7
ID_SIMPLE_BLOCK = 0xA3
ID_BLOCK_GROUP = 0xA0
ID_BLOCK = 0xA1

ID_CUES = 0x1C53BB6B
ID_CUE_POINT = 0xBB
ID_CUE_TIME = 0xB3
ID_CUE_TRACK_POSITIONS = 0xB7
ID_CUE_TRACK = 0xF7
ID_CUE_CLUSTER_POSITION = 0xF1

ID_SEEK_HEAD = 0x114D9B74
ID_VOID = 0xEC

# Matroska track type codes.
TRACK_TYPE_VIDEO = 1
TRACK_TYPE_AUDIO = 2
TRACK_TYPE_SUBTITLE = 17

SUPPORTED_CODECS = {"V_MPEG4/ISO/AVC", "A_AAC"}

# Top-level legal children of a Segment (used to end unknown-length Segments).
SEGMENT_CHILD_IDS = {
    0x114D9B74,  # SeekHead
    ID_SEGMENT_INFO,
    ID_TRACKS,
    ID_CLUSTER,
    ID_CUES,
    0x1254C367,  # Tags
    0x1043A770,  # Chapters
}
# Top-level legal children of a Cluster (used to end unknown-length Clusters).
CLUSTER_CHILD_IDS = {
    ID_CLUSTER_TIMECODE,
    ID_SIMPLE_BLOCK,
    ID_BLOCK_GROUP,
    0x75A1,  # PrevSize
    0xAB,    # Position
    0xA7,    # SilentTracks
    0x67,    # SilentTrackNumber
    0xE8,    # Position
    0xA451DF,  # EncryptedBlock
}
UNKNOWN_LENGTH_ALLOWED = {ID_SEGMENT, ID_CLUSTER}


class MatroskaError(ValueError):
    """Raised when the input is not a parseable, supported Matroska file."""


@dataclass(slots=True)
class Track:
    number: int
    uid: int | None
    type: int
    codec_id: str
    default_duration_ns: int | None = None
    codec_private: bytes | None = None
    pixel_width: int | None = None
    pixel_height: int | None = None
    frame_rate: float | None = None
    sampling_frequency: float | None = None
    channels: int | None = None
    bit_depth: int | None = None
    encrypted: bool = False
    header_compression: bool = False
    enabled: bool = True


@dataclass(slots=True)
class Sample:
    """One frame / audio packet located inside the source file."""

    track_number: int
    abs_time_ns: int      # cluster timecode + block relative timecode
    payload_offset: int   # absolute byte offset of frame payload in the file
    payload_size: int
    keyframe: bool


@dataclass(slots=True)
class MatroskaFile:
    timecode_scale_ns: int
    duration_ns: float | None
    tracks: list[Track]
    samples: list[Sample]
    tree: list[dict]
    warnings: list[str] = field(default_factory=list)
    cluster_count: int = 0
    cue_count: int = 0

    def samples_for(self, track_number: int) -> list[Sample]:
        return [s for s in self.samples if s.track_number == track_number]


def parse_block(
    reader: EbmlReader,
    block_element: Element,
    cluster_time_ns: int,
    scale_ns: int,
    *,
    simple: bool,
) -> list[Sample]:
    """Parse a Block / SimpleBlock payload into samples (no frame copies)."""
    end = block_element.end
    reader.seek(block_element.data_start)
    track_num, tn_len = reader.read_vint()
    rel_ms = int.from_bytes(reader._read_exact(2), "big", signed=True)
    flags = reader._read_exact(1)[0]
    keyframe = bool(flags & 0x80) if simple else False
    frames: list[tuple[int, int]] = []  # (offset, size)
    while reader.pos() < end:
        size, _ = reader.read_vint()
        payload_offset = reader.pos()
        if size < 0 or payload_offset + size > end:
            raise MatroskaError("block frame extends past block boundary")
        frames.append((payload_offset, size))
        reader.seek(payload_offset + size)
    if not frames:
        raise MatroskaError(f"block for track {track_num} contains no frames")
    if len(frames) > 64:
        raise MatroskaError(f"block lacing with {len(frames)} frames exceeds limit")
    abs_time_ns = cluster_time_ns + rel_ms * scale_ns
    return [
        Sample(track_num, abs_time_ns, offset, size, keyframe or not simple)
        for offset, size in frames
    ]

