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
    cluster_timecodes_ns: list[int] = field(default_factory=list)

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


# --------------------------------------------------------------------------
# High-level Segment parser
# --------------------------------------------------------------------------


def _bump(tree: list[dict], name: str, depth: int) -> None:
    """Maintain a compact outline: count siblings at the same depth."""
    if tree and tree[-1]["name"] == name and tree[-1]["depth"] == depth:
        tree[-1]["count"] += 1
    else:
        tree.append({"name": name, "depth": depth, "count": 1})


def parse_matroska(stream) -> MatroskaFile:
    reader = EbmlReader(stream, allowed_unknown_ids=UNKNOWN_LENGTH_ALLOWED)
    stream.seek(0, 2)
    file_size = stream.tell()
    from . import MAX_TOTAL_SAMPLES

    # -- EBML header -------------------------------------------------------
    header = reader.read_element_header(0)
    if header.id != ID_EBML_HEADER:
        raise MatroskaError("file does not start with an EBML Header element")
    tree: list[dict] = [{"name": "EBML", "depth": 0, "count": 1}]
    doc_type = None
    for child in reader.iter_children(header):
        if child.id == ID_EBML_DOC_TYPE:
            doc_type = reader.read_string(child)
        else:
            reader.read_payload(child)
    if doc_type not in ("matroska", "webm"):
        raise MatroskaError(f"unsupported DocType {doc_type!r} (need matroska/webm)")

    # -- exactly one Segment ----------------------------------------------
    seg = reader.read_element_header(0)
    if seg.id != ID_SEGMENT:
        raise MatroskaError("EBML Header must be followed by a Segment")
    _bump(tree, "Segment", 0)

    tracks: list[Track] = []
    samples: list[Sample] = []
    scale_ns = 1_000_000
    duration_ns: float | None = None
    cluster_count = 0
    cue_count = 0
    warnings: list[str] = []

    for top in reader.iter_children(seg, terminator_ids=set()):
        if top.id == ID_SEGMENT_INFO:
            _bump(tree, "Info", 1)
            for child in reader.iter_children(top):
                if child.id == ID_TIMECODE_SCALE:
                    scale_ns = reader.read_uint(child)
                    if scale_ns <= 0:
                        raise MatroskaError("TimecodeScale must be positive")
                elif child.id == ID_DURATION:
                    duration_ns = reader.read_float(child) * scale_ns
                else:
                    reader.read_payload(child)
        elif top.id == ID_TRACKS:
            _bump(tree, "Tracks", 1)
            for entry in reader.iter_children(top):
                if entry.id != ID_TRACK_ENTRY:
                    reader.read_payload(entry)
                    continue
                tracks.append(_parse_track(reader, entry, tree))
        elif top.id == ID_CLUSTER:
            cluster_count += 1
            _bump(tree, "Cluster", 1)
            cluster_tc_ns = None
            for child in reader.iter_children(
                top, terminator_ids={ID_CLUSTER, ID_CUES, ID_TRACKS,
                                     ID_SEGMENT_INFO, ID_SEEK_HEAD,
                                     0x1254C367, 0x1043A770}
            ):
                if child.id == ID_CLUSTER_TIMECODE:
                    cluster_tc_ns = reader.read_uint(child) * scale_ns
                    cluster_timecodes_ns.append(cluster_tc_ns)
                elif child.id == ID_SIMPLE_BLOCK:
                    if cluster_tc_ns is None:
                        raise MatroskaError("SimpleBlock before Cluster Timecode")
                    samples.extend(parse_block(
                        reader, child, cluster_tc_ns, scale_ns, simple=True))
                elif child.id == ID_BLOCK_GROUP:
                    if cluster_tc_ns is None:
                        raise MatroskaError("BlockGroup before Cluster Timecode")
                    samples.extend(
                        _parse_block_group(reader, child, cluster_tc_ns, scale_ns))
                else:
                    reader.read_payload(child)
            if len(samples) > MAX_TOTAL_SAMPLES:
                raise MatroskaError(f"sample count exceeds limit {MAX_TOTAL_SAMPLES}")
        elif top.id == ID_CUES:
            _bump(tree, "Cues", 1)
            for point in reader.iter_children(top):
                if point.id == ID_CUE_POINT:
                    cue_count += 1
                    for sub in reader.iter_children(point):
                        reader.read_payload(sub)
                else:
                    reader.read_payload(point)
        else:
            reader.read_payload(top)

    # Any trailing garbage after a bounded Segment is an error.
    if seg.known_size and reader.pos() < file_size:
        raise MatroskaError("trailing bytes after a single bounded Segment")

    if not tracks:
        raise MatroskaError("Segment contains no Track entries")
    if not samples:
        raise MatroskaError("Segment contains no media samples")
    return MatroskaFile(
        timecode_scale_ns=scale_ns,
        duration_ns=duration_ns,
        tracks=tracks,
        samples=samples,
        tree=tree,
        warnings=warnings,
        cluster_count=cluster_count,
        cue_count=cue_count,
        cluster_timecodes_ns=cluster_timecodes_ns,
    )


def _parse_track(reader: EbmlReader, entry: Element,
                 tree: list[dict]) -> Track:
    _bump(tree, "TrackEntry", 2)
    t = Track(number=-1, uid=None, type=0, codec_id="")
    for child in reader.iter_children(entry):
        cid = child.id
        if cid == ID_TRACK_NUMBER:
            t.number = reader.read_uint(child)
        elif cid == ID_TRACK_UID:
            t.uid = reader.read_uint(child)
        elif cid == ID_TRACK_TYPE:
            t.type = reader.read_uint(child)
        elif cid == ID_CODEC_ID:
            t.codec_id = reader.read_string(child)
        elif cid == ID_DEFAULT_DURATION:
            t.default_duration_ns = reader.read_uint(child)
        elif cid == ID_CODEC_PRIVATE:
            t.codec_private = reader.read_binary(child)
        elif cid == ID_FLAG_ENABLED:
            t.enabled = bool(reader.read_uint(child))
        elif cid == ID_VIDEO:
            _bump(tree, "Video", 3)
            for v in reader.iter_children(child):
                if v.id == ID_PIXEL_WIDTH:
                    t.pixel_width = reader.read_uint(v)
                elif v.id == ID_PIXEL_HEIGHT:
                    t.pixel_height = reader.read_uint(v)
                elif v.id == ID_FRAME_RATE:
                    t.frame_rate = reader.read_float(v)
                else:
                    reader.read_payload(v)
        elif cid == ID_AUDIO:
            _bump(tree, "Audio", 3)
            for a in reader.iter_children(child):
                if a.id == ID_SAMPLING_FREQUENCY:
                    t.sampling_frequency = reader.read_float(a)
                elif a.id == ID_CHANNELS:
                    t.channels = reader.read_uint(a)
                elif a.id == ID_BIT_DEPTH:
                    t.bit_depth = reader.read_uint(a)
                else:
                    reader.read_payload(a)
        elif cid == ID_CONTENT_ENCODINGS:
            _bump(tree, "ContentEncodings", 3)
            for enc in reader.iter_children(child):
                if enc.id != ID_CONTENT_ENCODING:
                    reader.read_payload(enc)
                    continue
                for e in reader.iter_children(enc):
                    if e.id == ID_CONTENT_ENCODING_TYPE and \
                            reader.read_uint(e) == 1:
                        t.header_compression = True
                    elif e.id == ID_CONTENT_ENC_ALGO and \
                            reader.read_uint(e) != 0:
                        t.encrypted = True
                    elif e.id == ID_CONTENT_COMPRESSION:
                        for k in reader.iter_children(e):
                            if k.id == ID_CONTENT_COMP_ALGO and \
                                    reader.read_uint(k) != 0:
                                t.header_compression = True
                            else:
                                reader.read_payload(k)
                    else:
                        reader.read_payload(e)
        else:
            reader.read_payload(child)
    if t.number < 0:
        raise MatroskaError("TrackEntry missing TrackNumber")
    return t


def _parse_block_group(reader: EbmlReader, group: Element,
                       cluster_tc_ns: int, scale_ns: int) -> list[Sample]:
    out: list[Sample] = []
    for child in reader.iter_children(group):
        if child.id == ID_BLOCK:
            out.extend(parse_block(reader, child, cluster_tc_ns,
                                   scale_ns, simple=False))
        else:
            reader.read_payload(child)
    return out
