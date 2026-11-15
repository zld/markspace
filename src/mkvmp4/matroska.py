"""Matroska container model: Segment, Info, Tracks, Clusters, Cues, blocks.

Only the structural subset needed for an auditable remux is modelled; the
parser keeps the raw codec private data and per-block timing so the later
timeline/codec layers can explain every rewrite.
"""

from __future__ import annotations

from dataclasses import dataclass, field

from . import ebml
from .ebml import EbmlParser, Element, Limits
from .errors import EbmlError, InputFormatError, LimitExceededError

# --- EBML header / Segment level -------------------------------------------
ID_DOC_TYPE = 0x4282
ID_SEEK_HEAD = 0x114D9B74
ID_INFO = 0x1549A966
ID_TRACKS = 0x1654AE6B
ID_CLUSTER = 0x1F43B675
ID_CUES = 0x1C53BB6B
ID_TAGS = 0x1254C367

SEGMENT_CHILD_IDS = frozenset({
    ID_SEEK_HEAD, ID_INFO, ID_TRACKS, ID_CLUSTER, ID_CUES, ID_TAGS,
    0x1043A770,  # Chapters
    0x18538067,  # defensive: nested Segment (illegal, will be rejected later)
})
CLUSTER_CHILD_IDS = frozenset({
    0xE7,         # Timestamp
    0xA3,         # SimpleBlock
    0xA0,         # BlockGroup
    0xA7,         # Position
    0xAB,         # PrevSize
    0x5854,       # SilentTracks
    0xAF,         # EncryptedBlock
})

# --- Info -------------------------------------------------------------------
ID_TIMECODE_SCALE = 0x2AD7B1
ID_DURATION = 0x4489
ID_TITLE = 0x7BA9

# --- Tracks -----------------------------------------------------------------
ID_TRACK_ENTRY = 0xAE
ID_TRACK_NUMBER = 0xD7
ID_TRACK_UID = 0x73C5
ID_TRACK_TYPE = 0x83
ID_FLAG_DEFAULT = 0x88
ID_FLAG_LACING = 0x9C
ID_DEFAULT_DURATION = 0x23E383
ID_TRACK_NAME = 0x536E
ID_LANGUAGE = 0x22B59C
ID_LANGUAGE_BCP47 = 0x22B59D
ID_CODEC_ID = 0x86
ID_CODEC_PRIVATE = 0x63A2
ID_AUDIO = 0xE1
ID_VIDEO = 0xE0
ID_CONTENT_ENCODINGS = 0x6D80
ID_SAMPLING_FREQ = 0xB5
ID_CHANNELS = 0x9F
ID_PIXEL_WIDTH = 0xB0
ID_PIXEL_HEIGHT = 0xBA

# --- Cluster / blocks -------------------------------------------------------
ID_CLUSTER_TIMECODE = 0xE7
ID_SIMPLE_BLOCK = 0xA3
ID_BLOCK_GROUP = 0xA0
ID_BLOCK = 0xA1
ID_BLOCK_DURATION = 0x9B
ID_REFERENCE_BLOCK = 0xFB
ID_ENCRYPTED_BLOCK = 0xAF

TRACK_TYPE_VIDEO = 1
TRACK_TYPE_AUDIO = 2
TRACK_TYPE_COMPLEX = 3
TRACK_TYPE_SUBTITLE = 17

# --- Cues -------------------------------------------------------------------
ID_CUE_POINT = 0xBB


@dataclass
class Track:
    number: int
    uid: int
    type: int
    codec_id: str
    codec_private: bytes | None = None
    flag_default: bool = True
    flag_lacing: bool = True
    default_duration_ns: int | None = None
    name: str = ""
    language: str = ""
    channels: int = 0
    sample_rate: float = 0.0
    pixel_width: int = 0
    pixel_height: int = 0


@dataclass
class Block:
    track_number: int
    relative_timecode: int          # signed int16, in TimestampScale units
    flags: int
    keyframe: bool
    lacing: int                     # 0 = none; anything else unsupported
    data: bytes
    pts_ns: int = 0
    duration_ns: int | None = None  # BlockDuration, when present


@dataclass
class Cluster:
    timecode_ns: int
    blocks: list[Block] = field(default_factory=list)
    position: int = 0
    unknown_length: bool = False


@dataclass
class MatroskaFile:
    path: str
    timestamp_scale_ns: int
    duration_ns: float | None
    title: str
    tracks: list[Track]
    clusters: list[Cluster]
    cues_present: bool
    element_count: int
    unknown_length_count: int
    segment_position: int
    segment_unknown_length: bool


def _children(parser: EbmlParser, parent: Element,
              valid_ids: frozenset[int] | None = None) -> list[Element]:
    if parent.known_length:
        kids = parser.iter_children(parent.body_start, parent.body_end)
    else:
        ids = valid_ids or frozenset()
        kids = ebml.scan_unknown_children(parser, parent, ids)
    return kids


def _first(elements: list[Element], eid: int) -> Element | None:
    return next((e for e in elements if e.id == eid), None)


def _require(element: Element | None, name: str,
             track_number: int | None = None) -> Element:
    if element is None:
        where = f" in track {track_number}" if track_number else ""
        raise InputFormatError(f"required element {name} missing{where}")
    return element


def parse_matrouska(data: bytes, path: str,
                    limits: Limits | None = None) -> MatroskaFile:
    parser = EbmlParser(data, limits)
    roots = parser.iter_children(0, len(data))
    if not roots or roots[0].id != ebml.ID_EBML_HEADER:
        raise EbmlError("file does not start with an EBML Header element",
                        offset=0)
    header_kids = _children(parser, roots[0])
    doc_type_el = _first(header_kids, ID_DOC_TYPE)
    doc_type = parser.read_string(_require(doc_type_el, "DocType"))
    if doc_type not in ("matroska", "webm"):
        raise InputFormatError(f"unsupported EBML DocType {doc_type!r}")
    segment = next((e for e in roots[1:] if e.id == ebml.ID_SEGMENT), None)
    if segment is None:
        raise InputFormatError("EBML document has no Segment element")
    if any(e.id not in (ebml.ID_EBML_HEADER, ebml.ID_SEGMENT)
           for e in roots):
        bad = next(e for e in roots
                   if e.id not in (ebml.ID_EBML_HEADER, ebml.ID_SEGMENT))
        raise EbmlError("unexpected element at EBML document root",
                        offset=bad.position, element_id=bad.id)

    seg_kids = _children(parser, segment, SEGMENT_CHILD_IDS)
    unknown_count = 0 if segment.known_length else 1
    scale_ns, duration_ns, title = _parse_info(parser, seg_kids)
    tracks, track_unknown = _parse_tracks(parser, seg_kids)
    unknown_count += track_unknown
    clusters, cluster_unknown = _parse_clusters(parser, seg_kids,
                                                tracks, scale_ns)
    unknown_count += cluster_unknown
    cues_present = any(e.id == ID_CUES for e in seg_kids)
    return MatroskaFile(
        path=path,
        timestamp_scale_ns=scale_ns,
        duration_ns=duration_ns,
        title=title,
        tracks=tracks,
        clusters=clusters,
        cues_present=cues_present,
        element_count=parser.limits.element_count,
        unknown_length_count=unknown_count,
        segment_position=segment.position,
        segment_unknown_length=not segment.known_length,
    )


def _parse_info(parser: EbmlParser, seg_kids: list[Element]
                ) -> tuple[int, float | None, str]:
    info = _first(seg_kids, ID_INFO)
    if info is None:
        return 1_000_000, None, ""
    kids = _children(parser, info)
    scale_el = _first(kids, ID_TIMECODE_SCALE)
    scale = parser.read_uint(scale_el) if scale_el else 1_000_000
    if scale <= 0:
        raise InputFormatError("TimestampScale must be positive")
    duration_el = _first(kids, ID_DURATION)
    duration = parser.read_float(duration_el) if duration_el else None
    if duration is not None:
        duration = duration * scale
    title_el = _first(kids, ID_TITLE)
    title = parser.read_string(title_el) if title_el else ""
    return scale, duration, title

