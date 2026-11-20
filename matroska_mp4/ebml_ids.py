"""Matroska/EBML element identifiers and parent-child hierarchy."""

from __future__ import annotations

# Top level ---------------------------------------------------------------
EBML = 0x1A45DFA3
EBML_VERSION = 0x4286
EBML_READ_VERSION = 0x42F7
EBML_MAX_ID_LENGTH = 0x42F2
EBML_MAX_SIZE_LENGTH = 0x42F3
DOC_TYPE = 0x4282
DOC_TYPE_VERSION = 0x4287
DOC_TYPE_READ_VERSION = 0x4285

SEGMENT = 0x18538067
SEEK_HEAD = 0x114D9B74
SEEK = 0x4DBB
SEEK_ID = 0x53AB
SEEK_POSITION = 0x53AC
INFO = 0x1549A966
TIMESTAMP_SCALE = 0x2AD7B1
DURATION = 0x4489
MUXING_APP = 0x4D80
WRITING_APP = 0x5741
TITLE = 0x7BA9
TRACKS = 0x1654AE6B
TRACK_ENTRY = 0xAE
TRACK_NUMBER = 0xD7
TRACK_UID = 0x73C5
TRACK_TYPE = 0x83
FLAG_LACING = 0x9C
DEFAULT_DURATION = 0x23E383
TRACK_TIMESTAMP_SCALE = 0x23314F
CODE_DELAY = 0x56AA
CONTENT_ENCODINGS = 0x6D80
MAX_BLOCK_ADDITION_ID = 0x55EE
NAME = 0x536E
LANGUAGE = 0x22B59C
CODEC_ID = 0x86
CODEC_PRIVATE = 0x63A2
VIDEO = 0xE0
PIXEL_WIDTH = 0xB0
PIXEL_HEIGHT = 0xBA
DISPLAY_WIDTH = 0x54B0
DISPLAY_HEIGHT = 0x54BA
AUDIO = 0xE1
SAMPLING_FREQUENCY = 0xB5
CHANNELS = 0x9F
BIT_DEPTH = 0x6264
CLUSTER = 0x1F43B675
TIMECODE = 0xE7
POSITION = 0xA7
PREV_SIZE = 0xAB
SILENT_TRACKS = 0x5854
SIMPLE_BLOCK = 0xA3
BLOCK_GROUP = 0xA0
BLOCK_ADDITIONS = 0x75A1
BLOCK = 0xA1
BLOCK_DURATION = 0x9B
REFERENCE_BLOCK = 0xFB
DISCARD_PADDING = 0x75A2
CUES = 0x1C53BB6B
CUE_POINT = 0xBB
CUE_TIME = 0xB3
CUE_TRACK_POSITIONS = 0xB7
CUE_TRACK = 0xF7
CUE_CLUSTER_POSITION = 0xF1
ATTACHMENTS = 0x1941A469
ATTACHED_FILE = 0x61A7
FILE_NAME = 0x466E
FILE_MIME_TYPE = 0x4660
FILE_DATA = 0x465C
CHAPTERS = 0x1043A770
TAGS = 0x1254C367
VOID = 0xEC
CRC32 = 0xBF

ELEMENT_NAMES = {
    EBML: "EBML", EBML_VERSION: "EBMLVersion", EBML_READ_VERSION: "EBMLReadVersion",
    EBML_MAX_ID_LENGTH: "EBMLMaxIDLength", EBML_MAX_SIZE_LENGTH: "EBMLMaxSizeLength",
    DOC_TYPE: "DocType", DOC_TYPE_VERSION: "DocTypeVersion",
    DOC_TYPE_READ_VERSION: "DocTypeReadVersion", SEGMENT: "Segment",
    SEEK_HEAD: "SeekHead", SEEK: "Seek", SEEK_ID: "SeekID", SEEK_POSITION: "SeekPosition",
    INFO: "Info", TIMESTAMP_SCALE: "TimestampScale", DURATION: "Duration",
    MUXING_APP: "MuxingApp", WRITING_APP: "WritingApp", TITLE: "Title",
    TRACKS: "Tracks", TRACK_ENTRY: "TrackEntry", TRACK_NUMBER: "TrackNumber",
    TRACK_UID: "TrackUID", TRACK_TYPE: "TrackType", FLAG_LACING: "FlagLacing",
    DEFAULT_DURATION: "DefaultDuration", TRACK_TIMESTAMP_SCALE: "TrackTimestampScale",
    CODE_DELAY: "CodecDelay", CONTENT_ENCODINGS: "ContentEncodings",
    MAX_BLOCK_ADDITION_ID: "MaxBlockAdditionID",
    NAME: "Name", LANGUAGE: "Language",
    CODEC_ID: "CodecID", CODEC_PRIVATE: "CodecPrivate", VIDEO: "Video",
    PIXEL_WIDTH: "PixelWidth", PIXEL_HEIGHT: "PixelHeight",
    DISPLAY_WIDTH: "DisplayWidth", DISPLAY_HEIGHT: "DisplayHeight",
    AUDIO: "Audio", SAMPLING_FREQUENCY: "SamplingFrequency", CHANNELS: "Channels",
    BIT_DEPTH: "BitDepth", CLUSTER: "Cluster", TIMECODE: "Timecode",
    POSITION: "Position", PREV_SIZE: "PrevSize", SILENT_TRACKS: "SilentTracks",
    SIMPLE_BLOCK: "SimpleBlock", BLOCK_GROUP: "BlockGroup", BLOCK: "Block",
    BLOCK_ADDITIONS: "BlockAdditions",
    BLOCK_DURATION: "BlockDuration", REFERENCE_BLOCK: "ReferenceBlock",
    DISCARD_PADDING: "DiscardPadding", CUES: "Cues", CUE_POINT: "CuePoint",
    CUE_TIME: "CueTime", CUE_TRACK_POSITIONS: "CueTrackPositions",
    CUE_TRACK: "CueTrack", CUE_CLUSTER_POSITION: "CueClusterPosition",
    ATTACHMENTS: "Attachments", ATTACHED_FILE: "AttachedFile", FILE_NAME: "FileName",
    FILE_MIME_TYPE: "FileMimeType", FILE_DATA: "FileData", CHAPTERS: "Chapters",
    TAGS: "Tags", VOID: "Void", CRC32: "CRC32",
}

# Children allowed directly under a Segment.
SEGMENT_LEVEL = frozenset({SEEK_HEAD, INFO, TRACKS, CLUSTER, CUES, ATTACHMENTS,
                           CHAPTERS, TAGS, VOID, CRC32})
# Children allowed directly under a Cluster.  This set is also used to find
# the end of an unknown-length Cluster: the first Segment-level element ends it.
CLUSTER_LEVEL = frozenset({TIMECODE, POSITION, PREV_SIZE, SILENT_TRACKS,
                           SIMPLE_BLOCK, BLOCK_GROUP, VOID, CRC32})
BLOCK_GROUP_LEVEL = frozenset({BLOCK, BLOCK_ADDITIONS, BLOCK_DURATION,
                               REFERENCE_BLOCK, DISCARD_PADDING, VOID, CRC32})
INFO_LEVEL = frozenset({TIMESTAMP_SCALE, DURATION, MUXING_APP, WRITING_APP,
                        TITLE, VOID, CRC32})
TRACKS_LEVEL = frozenset({TRACK_ENTRY, VOID, CRC32})
TRACK_ENTRY_LEVEL = frozenset({
    TRACK_NUMBER, TRACK_UID, TRACK_TYPE, FLAG_LACING, DEFAULT_DURATION,
    TRACK_TIMESTAMP_SCALE, CODE_DELAY, CONTENT_ENCODINGS, MAX_BLOCK_ADDITION_ID,
    NAME, LANGUAGE, CODEC_ID, CODEC_PRIVATE, VIDEO, AUDIO, VOID, CRC32})
VIDEO_LEVEL = frozenset({PIXEL_WIDTH, PIXEL_HEIGHT, DISPLAY_WIDTH,
                         DISPLAY_HEIGHT, VOID, CRC32})
AUDIO_LEVEL = frozenset({SAMPLING_FREQUENCY, CHANNELS, BIT_DEPTH, VOID, CRC32})
EBML_LEVEL = frozenset({EBML_VERSION, EBML_READ_VERSION, EBML_MAX_ID_LENGTH,
                        EBML_MAX_SIZE_LENGTH, DOC_TYPE, DOC_TYPE_VERSION,
                        DOC_TYPE_READ_VERSION, VOID, CRC32})
CUES_LEVEL = frozenset({CUE_POINT, VOID, CRC32})
CUE_POINT_LEVEL = frozenset({CUE_TIME, CUE_TRACK_POSITIONS, VOID, CRC32})
CUE_TRACK_POSITIONS_LEVEL = frozenset({CUE_TRACK, CUE_CLUSTER_POSITION, VOID, CRC32})
ATTACHMENTS_LEVEL = frozenset({ATTACHED_FILE, VOID, CRC32})
ATTACHED_FILE_LEVEL = frozenset({FILE_NAME, FILE_MIME_TYPE, FILE_DATA, VOID, CRC32})
SEEK_HEAD_LEVEL = frozenset({SEEK, VOID, CRC32})
SEEK_LEVEL = frozenset({SEEK_ID, SEEK_POSITION, VOID, CRC32})

MASTER_CHILDREN = {
    EBML: EBML_LEVEL,
    SEGMENT: SEGMENT_LEVEL,
    INFO: INFO_LEVEL,
    TRACKS: TRACKS_LEVEL,
    TRACK_ENTRY: TRACK_ENTRY_LEVEL,
    VIDEO: VIDEO_LEVEL,
    AUDIO: AUDIO_LEVEL,
    CLUSTER: CLUSTER_LEVEL,
    BLOCK_GROUP: BLOCK_GROUP_LEVEL,
    CUES: CUES_LEVEL,
    CUE_POINT: CUE_POINT_LEVEL,
    CUE_TRACK_POSITIONS: CUE_TRACK_POSITIONS_LEVEL,
    ATTACHMENTS: ATTACHMENTS_LEVEL,
    ATTACHED_FILE: ATTACHED_FILE_LEVEL,
    SEEK_HEAD: SEEK_HEAD_LEVEL,
    SEEK: SEEK_LEVEL,
}
UNKNOWN_SIZE_IDS = frozenset({SEGMENT, CLUSTER})
PRESENTATION_LEAF_IDS = frozenset({SIMPLE_BLOCK, BLOCK, FILE_DATA})
PRESENTATION_ELEMENTS = frozenset({SIMPLE_BLOCK, BLOCK_GROUP})


def element_name(element_id: int) -> str:
    """Human-readable element name, stable for unknown IDs."""

    return ELEMENT_NAMES.get(element_id, f"Unknown_0x{element_id:X}")


def encode_id(element_id: int) -> bytes:
    """Return the canonical four-or-fewer byte EBML identifier."""

    if element_id < 0:
        raise ValueError("negative element ID")
    for length in (1, 2, 3, 4):
        if element_id < (1 << (8 * length)):
            raw = element_id.to_bytes(length, "big")
            if raw[0] == 0:
                break
            return raw
    raise ValueError(f"invalid EBML element ID 0x{element_id:X}")
