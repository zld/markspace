"""Matroska/EBML element schema: IDs, value types, hierarchy and boundaries.

Only the subset of the Matroska spec needed by the remuxer is modelled.
Unknown IDs are not rejected: the reader treats them as opaque payload so a
file with newer/foreign elements still parses, but every *known* ID carries
its type and parent rules here.
"""

from enum import Enum

from ..errors import CorruptMKVError


class ElemType(str, Enum):
    """EBML element content types."""

    MASTER = "master"
    UINT = "uint"
    SINT = "sint"
    FLOAT = "float"
    STRING = "string"
    UTF8 = "utf8"
    BINARY = "binary"
    DATE = "date"


# ---------------------------------------------------------------------------
# Element IDs (hex values as used on the wire).
# ---------------------------------------------------------------------------
# EBML header
EBML = 0x1A45DFA3
EBML_READ_VERSION = 0x42F7
EBML_DOC_TYPE = 0x4282
EBML_DOC_TYPE_VERSION = 0x4287
# Segment level
SEGMENT = 0x18538067
SEEK_HEAD = 0x114D9B74
INFO = 0x1549A966
TIMESTAMP_SCALE = 0x2AD7B1
DURATION = 0x4489
MUXING_APP = 0x4D80
WRITING_APP = 0x5741
SEGMENT_UID = 0x73A4
TRACKS = 0x1654AE6B
TRACK_ENTRY = 0xAE
TRACK_NUMBER = 0xD7
TRACK_UID = 0x73C5
TRACK_TYPE = 0x83
FLAG_DEFAULT = 0x88
FLAG_LACING = 0x9C
DEFAULT_DURATION = 0x23E383
NAME = 0x536E
LANGUAGE = 0x22B59C
LANGUAGE_BCP47 = 0x22B59D
CODEC_ID = 0x86
CODEC_PRIVATE = 0x63A2
# Audio / video properties
VIDEO = 0xE0
PIXEL_WIDTH = 0xB0
PIXEL_HEIGHT = 0xBA
AUDIO = 0xE1
SAMPLING_FREQUENCY = 0xB5
CHANNELS = 0x9F
BIT_DEPTH = 0x6264
# Clusters and blocks
CLUSTER = 0x1F43B675
TIMESTAMP = 0xE7
SIMPLE_BLOCK = 0xA3
BLOCK_GROUP = 0xA0
BLOCK = 0xA1
BLOCK_DURATION = 0x9B
REFERENCE_BLOCK = 0xFB
BLOCK_ADDITIONS = 0x75A1
BLOCK_MORE = 0xA6
BLOCK_ADDITIONAL = 0xA5
# Cues (index) — parsed only as opaque masters in this project.
CUES = 0x1C53BB6B
# Tags/Chapters are tolerated as unknown master content.


class ElementSpec:
    """Schema entry for one known EBML element."""

    __slots__ = ("element_id", "name", "elem_type", "parents", "unique")

    def __init__(self, element_id: int, name: str, elem_type: ElemType,
                 parents: frozenset[int], unique: bool = False):
        self.element_id = element_id
        self.name = name
        self.elem_type = elem_type
        self.parents = parents
        # ``unique`` elements may appear at most once inside their parent.
        self.unique = unique

    def __repr__(self) -> str:
        return f"ElementSpec({self.name!r}, {self.elem_type.value})"


def _spec(element_id: int, name: str, elem_type: ElemType,
          parents: tuple[int, ...], unique: bool = False) -> ElementSpec:
    return ElementSpec(element_id, name, elem_type, frozenset(parents), unique)


# ID -> schema spec.
SPEC: dict[int, ElementSpec] = {
    spec.element_id: spec
    for spec in (
        _spec(EBML, "EBML", ElemType.MASTER, ()),
        _spec(EBML_READ_VERSION, "EBMLReadVersion", ElemType.UINT, (EBML,)),
        _spec(EBML_DOC_TYPE, "DocType", ElemType.STRING, (EBML,), unique=True),
        _spec(EBML_DOC_TYPE_VERSION, "DocTypeVersion", ElemType.UINT, (EBML,)),
        _spec(SEGMENT, "Segment", ElemType.MASTER, ()),
        _spec(SEEK_HEAD, "SeekHead", ElemType.MASTER, (SEGMENT,)),
        _spec(INFO, "Info", ElemType.MASTER, (SEGMENT,)),
        _spec(TIMESTAMP_SCALE, "TimestampScale", ElemType.UINT, (INFO,), unique=True),
        _spec(DURATION, "Duration", ElemType.FLOAT, (INFO,)),
        _spec(MUXING_APP, "MuxingApp", ElemType.UTF8, (INFO,)),
        _spec(WRITING_APP, "WritingApp", ElemType.UTF8, (INFO,)),
        _spec(SEGMENT_UID, "SegmentUID", ElemType.BINARY, (INFO,)),
        _spec(TRACKS, "Tracks", ElemType.MASTER, (SEGMENT,)),
        _spec(TRACK_ENTRY, "TrackEntry", ElemType.MASTER, (TRACKS,)),
        _spec(TRACK_NUMBER, "TrackNumber", ElemType.UINT, (TRACK_ENTRY,), unique=True),
        _spec(TRACK_UID, "TrackUID", ElemType.UINT, (TRACK_ENTRY,)),
        _spec(TRACK_TYPE, "TrackType", ElemType.UINT, (TRACK_ENTRY,), unique=True),
        _spec(FLAG_DEFAULT, "FlagDefault", ElemType.UINT, (TRACK_ENTRY,)),
        _spec(FLAG_LACING, "FlagLacing", ElemType.UINT, (TRACK_ENTRY,)),
        _spec(DEFAULT_DURATION, "DefaultDuration", ElemType.UINT, (TRACK_ENTRY,)),
        _spec(NAME, "Name", ElemType.UTF8, (TRACK_ENTRY,)),
        _spec(LANGUAGE, "Language", ElemType.STRING, (TRACK_ENTRY,)),
        _spec(LANGUAGE_BCP47, "LanguageBCP47", ElemType.STRING, (TRACK_ENTRY,)),
        _spec(CODEC_ID, "CodecID", ElemType.STRING, (TRACK_ENTRY,), unique=True),
        _spec(CODEC_PRIVATE, "CodecPrivate", ElemType.BINARY, (TRACK_ENTRY,)),
        _spec(VIDEO, "Video", ElemType.MASTER, (TRACK_ENTRY,)),
        _spec(PIXEL_WIDTH, "PixelWidth", ElemType.UINT, (VIDEO,), unique=True),
        _spec(PIXEL_HEIGHT, "PixelHeight", ElemType.UINT, (VIDEO,), unique=True),
        _spec(AUDIO, "Audio", ElemType.MASTER, (TRACK_ENTRY,)),
        _spec(SAMPLING_FREQUENCY, "SamplingFrequency", ElemType.FLOAT, (AUDIO,)),
        _spec(CHANNELS, "Channels", ElemType.UINT, (AUDIO,)),
        _spec(BIT_DEPTH, "BitDepth", ElemType.UINT, (AUDIO,)),
        _spec(CLUSTER, "Cluster", ElemType.MASTER, (SEGMENT,)),
        _spec(TIMESTAMP, "Timestamp", ElemType.UINT,
              (CLUSTER, INFO), unique=True),
        _spec(SIMPLE_BLOCK, "SimpleBlock", ElemType.BINARY, (CLUSTER,)),
        _spec(BLOCK_GROUP, "BlockGroup", ElemType.MASTER, (CLUSTER,)),
        _spec(BLOCK, "Block", ElemType.BINARY, (BLOCK_GROUP,), unique=True),
        _spec(BLOCK_DURATION, "BlockDuration", ElemType.UINT, (BLOCK_GROUP,)),
        _spec(REFERENCE_BLOCK, "ReferenceBlock", ElemType.SINT, (BLOCK_GROUP,)),
        _spec(BLOCK_ADDITIONS, "BlockAdditions", ElemType.MASTER, (BLOCK_GROUP,)),
        _spec(BLOCK_MORE, "BlockMore", ElemType.MASTER, (BLOCK_ADDITIONS,)),
        _spec(BLOCK_ADDITIONAL, "BlockAdditional", ElemType.BINARY, (BLOCK_MORE,)),
        _spec(CUES, "Cues", ElemType.MASTER, (SEGMENT,)),
    )
}

#: Top-level Segment children that terminate an unknown-size Cluster.
SEGMENT_CHILD_IDS: frozenset[int] = frozenset({
    SEEK_HEAD, INFO, TRACKS, CLUSTER, CUES,
})
#: Top-level siblings that terminate an unknown-size Segment (end of file).
SEGMENT_TOP_IDS: frozenset[int] = frozenset({EBML, SEGMENT})


def spec_for(element_id: int) -> ElementSpec | None:
    """Return the schema spec for ``element_id`` or ``None`` if unknown."""
    return SPEC.get(element_id)


def name_for(element_id: int) -> str:
    """Human-readable element name; unknown IDs get a hex placeholder."""
    spec = SPEC.get(element_id)
    return spec.name if spec is not None else f"Unknown_{element_id:08X}"


def is_master(element_id: int) -> bool:
    """Whether a known ID opens a master element (unknown IDs are leaves)."""
    spec = SPEC.get(element_id)
    return spec is not None and spec.elem_type is ElemType.MASTER


def check_parent(child_id: int, parent_id: int | None) -> None:
    """Validate that ``child_id`` may legally appear inside ``parent_id``.

    Unknown children are accepted (forward compatibility). A *known* child
    used outside every one of its declared parents is a structural error.
    """
    spec = SPEC.get(child_id)
    if spec is None or not spec.parents:
        return
    if parent_id is None or parent_id not in spec.parents:
        raise CorruptMKVError(
            f"element {spec.name} (0x{child_id:X}) is not allowed inside "
            f"{name_for(parent_id) if parent_id is not None else 'the root'}")


#: CodecID strings accepted by the remuxer, mapped to their MP4 four-CC.
#: Only H.264/AVC video and AAC audio are repackaged; everything else is
#: rejected explicitly (see UnsupportedCodecError in segment.py).
SUPPORTED_CODECS: dict[str, str] = {
    "V_MPEG4/ISO/AVC": "avc1",
    "A_AAC": "mp4a",
}

#: Matroska TrackType codes.
TRACK_TYPE_VIDEO = 1
TRACK_TYPE_AUDIO = 2
