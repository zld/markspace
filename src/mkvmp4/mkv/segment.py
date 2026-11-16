"""Matroska Segment-level parsing: EBML header, Info, Tracks, Clusters.

This module turns the generic EBML stream into domain objects:

* :class:`TrackInfo` for every TrackEntry (codec, language, codec private);
* :class:`RawBlock` for every SimpleBlock / BlockGroup body located in the
  file. Block payload de-interlacing happens in :mod:`mkvmp4.mkv.blocks`.

Only H.264/AVC and AAC are accepted; any other codec raises
:class:`~mkvmp4.errors.UnsupportedCodecError` listing every offending track
number so the CLI can reject the file up front.
"""

from dataclasses import dataclass, field

from ..ebml import schema
from ..ebml.reader import EbmlReader, decode_leaf
from ..errors import CorruptMKVError, UnsupportedCodecError

#: Matroska default TimestampScale (nanoseconds per tick).
DEFAULT_TIMESTAMP_SCALE = 1_000_000


@dataclass(slots=True)
class TrackInfo:
    """One parsed TrackEntry."""

    number: int                      # Matroska TrackNumber (block reference)
    uid: int | None
    track_type: int                  # 1 = video, 2 = audio
    codec_id: str
    mp4_codec: str                   # mapped four-CC, e.g. "avc1"
    language: str = "und"
    default_duration: int | None = None   # nanoseconds per sample
    flag_lacing: bool = True
    codec_private: bytes = b""
    pixel_width: int | None = None
    pixel_height: int | None = None
    sampling_frequency: float | None = None
    channels: int | None = None
    bit_depth: int | None = None


@dataclass(slots=True)
class RawBlock:
    """Location of one block element plus its container metadata."""

    track_number: int
    cluster_timecode: int            # Cluster Timestamp in timescale ticks
    element_offset: int              # absolute offset of the block element
    data_offset: int                 # absolute offset of the block *body*
    size: int                        # block body length
    in_block_group: bool
    keyframe: bool                   # SimpleBlock flag / inferred otherwise
    duration_ticks: int | None       # BlockGroup BlockDuration, if present
    reference_count: int             # ReferenceBlock count (0 => key frame)


@dataclass(slots=True)
class MatroskaFile:
    """Result of a full structural parse."""

    path: str
    timestamp_scale: int
    duration_ticks: float | None
    tracks: list[TrackInfo] = field(default_factory=list)
    blocks: list[RawBlock] = field(default_factory=list)
    unknown_size_masters: int = 0
    cluster_count: int = 0

    def track_by_number(self, number: int) -> TrackInfo:
        for track in self.tracks:
            if track.number == number:
                return track
        raise CorruptMKVError(f"block references unknown track number {number}")


def _read_ebml_header(reader: EbmlReader) -> None:
    """Validate the leading EBML element declares Matroska content."""
    root = reader.next_element()
    if root is None or root.element_id != schema.EBML:
        raise CorruptMKVError(
            "file does not start with an EBML header element",
            offset=0)
    doc_type = None
    reader.open_master(root)
    while (child := reader.next_element()) is not None:
        if child.element_id == schema.EBML_DOC_TYPE:
            doc_type = decode_leaf(child, reader)
    reader.close_master()
    if doc_type not in ("matroska", "webm"):
        raise CorruptMKVError(f"unsupported DocType {doc_type!r} (need matroska)")


def _parse_track_entry(reader: EbmlReader, entry) -> TrackInfo:
    values: dict = {}
    codec_private = b""
    reader.open_master(entry)
    while (child := reader.next_element()) is not None:
        eid = child.element_id
        if eid == schema.CODEC_PRIVATE:
            codec_private = reader.read_payload(child)
        elif eid in (schema.VIDEO, schema.AUDIO):
            reader.open_master(child)
            while (prop := reader.next_element()) is not None:
                values[prop.element_id] = decode_leaf(prop, reader)
            reader.close_master()
        elif schema.spec_for(eid) is not None:
            values[eid] = decode_leaf(child, reader)
        # unknown children are skipped by frame resume automatically
    reader.close_master()

    number = values.get(schema.TRACK_NUMBER)
    codec_id = values.get(schema.CODEC_ID, "")
    if number is None:
        raise CorruptMKVError("TrackEntry without TrackNumber")
    if not codec_id:
        raise CorruptMKVError(f"track {number} has no CodecID")
    if codec_id not in schema.SUPPORTED_CODECS:
        # Raised per-track; the caller aggregates all offenders.
        raise UnsupportedCodecError(
            f"codec {codec_id!r} cannot be remuxed", [number])
    language = values.get(schema.LANGUAGE_BCP47) or values.get(schema.LANGUAGE) or "und"
    return TrackInfo(
        number=number,
        uid=values.get(schema.TRACK_UID),
        track_type=values.get(schema.TRACK_TYPE, 0),
        codec_id=codec_id,
        mp4_codec=schema.SUPPORTED_CODECS[codec_id],
        language=language,
        default_duration=values.get(schema.DEFAULT_DURATION),
        flag_lacing=bool(values.get(schema.FLAG_LACING, 1)),
        codec_private=codec_private,
        pixel_width=values.get(schema.PIXEL_WIDTH),
        pixel_height=values.get(schema.PIXEL_HEIGHT),
        sampling_frequency=values.get(schema.SAMPLING_FREQUENCY),
        channels=values.get(schema.CHANNELS),
        bit_depth=values.get(schema.BIT_DEPTH),
    )


def _parse_cluster(reader: EbmlReader, cluster, mkv: MatroskaFile) -> None:
    mkv.cluster_count += 1
    if cluster.unknown_size:
        mkv.unknown_size_masters += 1
    cluster_tc = 0
    reader.open_master(cluster)
    while (child := reader.next_element()) is not None:
        if child.element_id == schema.TIMESTAMP:
            cluster_tc = decode_leaf(child, reader)
        elif child.element_id == schema.SIMPLE_BLOCK:
            body = reader.read_payload(child)
            keyframe = bool(body[2] & 0x80) if len(body) >= 3 else False
            mkv.blocks.append(RawBlock(
                track_number=-1,  # resolved in blocks.py from the body
                cluster_timecode=cluster_tc,
                element_offset=child.offset,
                data_offset=child.data_offset,
                size=child.size,
                in_block_group=False,
                keyframe=keyframe,
                duration_ticks=None,
                reference_count=0,
            ))
        elif child.element_id == schema.BLOCK_GROUP:
            _parse_block_group(reader, child, cluster_tc, mkv)
        # other cluster children (PrevSize, Position...) are ignored
    reader.close_master()


def _parse_block_group(reader: EbmlReader, group, cluster_tc: int,
                       mkv: MatroskaFile) -> None:
    duration = None
    references = 0
    block_element = None
    reader.open_master(group)
    while (child := reader.next_element()) is not None:
        if child.element_id == schema.BLOCK:
            block_element = child
        elif child.element_id == schema.BLOCK_DURATION:
            duration = decode_leaf(child, reader)
        elif child.element_id == schema.REFERENCE_BLOCK:
            references += 1
        elif child.element_id == schema.BLOCK_ADDITIONS:
            reader.open_master(child)
            while (more := reader.next_element()) is not None:
                # BlockMore subtree: skip structurally
                reader.open_master(more)
                while reader.next_element() is not None:
                    pass
                reader.close_master()
            reader.close_master()
    reader.close_master()
    if block_element is None:
        raise CorruptMKVError("BlockGroup without Block element")
    mkv.blocks.append(RawBlock(
        track_number=-1,
        cluster_timecode=cluster_tc,
        element_offset=block_element.offset,
        data_offset=block_element.data_offset,
        size=block_element.size,
        in_block_group=True,
        keyframe=references == 0,
        duration_ticks=duration,
        reference_count=references,
    ))


def parse_matroska(path: str, *, max_samples: int,
                   max_packet_size: int) -> MatroskaFile:
    """Parse a Matroska file into a :class:`MatroskaFile` result.

    ``max_samples`` bounds the number of *samples* (validated after lacing in
    the blocks stage); ``max_packet_size`` bounds individual block bodies.
    """
    mkv = MatroskaFile(
        path=path,
        timestamp_scale=DEFAULT_TIMESTAMP_SCALE,
        duration_ticks=None,
    )
    rejected: list[int] = []
    rejected_messages: list[str] = []
    with EbmlReader(path, max_element_size=max_packet_size) as reader:
        reader.open_root()
        _read_ebml_header(reader)
        segment = None
        while (root := reader.next_element()) is not None:
            if root.element_id == schema.SEGMENT:
                segment = root
                break
        if segment is None:
            raise CorruptMKVError("no Segment element found")
        if segment.unknown_size:
            mkv.unknown_size_masters += 1

        reader.open_master(segment)
        tracks_seen = False
        while (top := reader.next_element()) is not None:
            if top.element_id == schema.INFO:
                _parse_info(reader, top, mkv)
            elif top.element_id == schema.TRACKS:
                tracks_seen = True
                _parse_tracks(reader, top, mkv, rejected, rejected_messages)
            elif top.element_id == schema.CLUSTER:
                _parse_cluster(reader, top, mkv)
            else:
                # SeekHead, Cues, Tags, unknown top-level masters: walk them
                # so the frame cursor stays consistent, discarding contents.
                _drain(reader, top)
        reader.close_master()

    if rejected:
        raise UnsupportedCodecError(
            "; ".join(rejected_messages) or "unsupported codec", rejected)
    if not tracks_seen or not mkv.tracks:
        raise CorruptMKVError("Segment contains no usable tracks")
    return mkv


def _parse_info(reader: EbmlReader, info, mkv: MatroskaFile) -> None:
    reader.open_master(info)
    while (child := reader.next_element()) is not None:
        if child.element_id == schema.TIMESTAMP_SCALE:
            mkv.timestamp_scale = decode_leaf(child, reader)
        elif child.element_id == schema.DURATION:
            mkv.duration_ticks = decode_leaf(child, reader)
    reader.close_master()


def _parse_tracks(reader: EbmlReader, tracks, mkv: MatroskaFile,
                  rejected: list[int], messages: list[str]) -> None:
    reader.open_master(tracks)
    while (entry := reader.next_element()) is not None:
        if entry.element_id != schema.TRACK_ENTRY:
            continue
        try:
            mkv.tracks.append(_parse_track_entry(reader, entry))
        except UnsupportedCodecError as exc:
            rejected.extend(exc.track_numbers)
            messages.append(str(exc))
    reader.close_master()


def _drain(reader: EbmlReader, element) -> None:
    """Consume a subtree without interpreting it (unknown/ignored master)."""
    if element.unknown_size or schema.is_master(element.element_id):
        reader.open_master(element)
        while (child := reader.next_element()) is not None:
            if child.unknown_size or schema.is_master(child.element_id):
                _drain(reader, child)
        reader.close_master()
