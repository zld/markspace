"""Matroska semantic layer: Segment, Info, Tracks and block extraction.

Built directly on :mod:`mkvmp4.ebml` / :mod:`mkvmp4.tree`; no third-party
container parser is involved.  Media payloads are never copied into
memory during inspection: blocks carry offsets into the input file.
"""

from __future__ import annotations

from dataclasses import dataclass, field

from .ebml import (
    decode_float,
    decode_int,
    decode_string,
    decode_uint,
    read_vint_id,
    read_vint_size,
)
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

# The exact codec subset this build can mux without transcoding.  The MP4
# sample entry for each ID lives in :mod:`mkvmp4.mp4`; both tables must agree,
# which ``_check_codec_tables`` enforces at import time.
SUPPORTED_CODECS: dict[str, tuple[str, str]] = {
    "V_MPEG4/ISO/AVC": ("video", "avc1"),
    "A_AAC": ("audio", "mp4a"),
    "S_TEXT/UTF8": ("subtitle", "tx3g"),
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


def _parse_header(doc_data: bytes, base: int) -> tuple[str, int]:
    """Read the EBML header: DocType and declared size limits."""
    if not doc_data:
        raise InputError("empty input: no EBML header", offset=base)
    header_nodes, _ = parse_tree(doc_data, base=base, max_depth=8, max_elements=256)
    if not header_nodes or header_nodes[0].element_id != ID_EBML:
        raise InputError(
            "input does not start with an EBML header (expected 0x1A45DFA3)",
            offset=base,
            path="/",
        )
    ebml = header_nodes[0]
    doc_type_node = _find_child(ebml.children, ID_DOCTYPE)
    doc_type = (
        decode_string(doc_data, doc_type_node.data_offset, doc_type_node.size)
        if doc_type_node
        else "matroska"
    )
    return doc_type, ebml.end_offset


def parse_matroska(data: bytes, *, source: str = "<memory>", base: int = 0) -> MatroskaFile:
    """Parse a complete Matroska byte string into tracks and clustered blocks."""
    doc_type, after_header = _parse_header(data, base)
    if doc_type not in ("matroska", "webm"):
        raise UnsupportedError(
            f"unsupported DocType {doc_type!r}: only matroska/webm are accepted",
            offset=base,
            path="/EBML/DocType",
        )

    top_nodes, stats = parse_tree(data, base=base, start=after_header)
    segment = None
    for node in top_nodes:
        if node.element_id == ID_SEGMENT:
            segment = node
            break
    if segment is None:
        raise InputError(
            "no Segment element found after the EBML header",
            offset=base + after_header,
            path="/",
        )

    timestamp_scale = DEFAULT_TIMESTAMP_SCALE
    muxing_app = writing_app = None
    tracks: list[Track] = []
    clusters: list[Cluster] = []

    for child in segment.children:
        if child.element_id == ID_INFO:
            for item in child.children:
                if item.element_id == ID_TIMESTAMP_SCALE:
                    timestamp_scale = _uint_field(data, item, base)
                elif item.element_id == ID_MUXING_APP:
                    muxing_app = decode_string(data, item.data_offset, item.size)
                elif item.element_id == ID_WRITING_APP:
                    writing_app = decode_string(data, item.data_offset, item.size)
        elif child.element_id == ID_TRACKS:
            tracks.extend(_parse_tracks(data, child, base))
        elif child.element_id == ID_CLUSTER:
            clusters.append(_parse_cluster(data, child, base))

    if timestamp_scale == 0:
        raise UnsupportedError(
            "TimestampScale is zero; cannot convert cluster timestamps",
            offset=base + segment.data_offset,
            path="/Segment/Info/TimestampScale",
        )

    return MatroskaFile(
        data=data,
        path=source,
        doc_type=doc_type,
        timestamp_scale=timestamp_scale,
        segment_offset=segment.offset,
        segment_data_offset=segment.data_offset,
        segment_end=segment.end_offset,
        segment_unknown_size=segment.unknown_size,
        tracks=tracks,
        clusters=clusters,
        muxing_app=muxing_app,
        writing_app=writing_app,
        tree_stats=stats,
    )


def _parse_tracks(data: bytes, tracks_node: RawNode, base: int) -> list[Track]:
    seen: set[int] = set()
    tracks: list[Track] = []
    for entry in tracks_node.children:
        if entry.element_id != ID_TRACK_ENTRY:
            continue
        number_node = _find_child(entry.children, ID_TRACK_NUMBER)
        if number_node is None:
            raise InputError(
                "TrackEntry without TrackNumber",
                offset=base + entry.offset,
                path=entry.path,
            )
        number = _uint_field(data, number_node, base)
        if number == 0:
            raise InputError(
                "TrackNumber 0 is reserved and invalid",
                offset=base + number_node.offset,
                path=number_node.path,
            )
        if number in seen:
            raise InputError(
                f"duplicate TrackNumber {number}",
                offset=base + entry.offset,
                path=entry.path,
                track=number,
            )
        seen.add(number)

        type_node = _find_child(entry.children, ID_TRACK_TYPE)
        codec_node = _find_child(entry.children, ID_CODEC_ID)
        if type_node is None or codec_node is None:
            raise InputError(
                "TrackEntry requires both TrackType and CodecID",
                offset=base + entry.offset,
                path=entry.path,
                track=number,
            )
        track_type = _uint_field(data, type_node, base)
        codec_id = decode_string(data, codec_node.data_offset, codec_node.size)
        if track_type not in TRACK_TYPE_NAMES:
            raise UnsupportedError(
                f"track {number}: unsupported TrackType {track_type}",
                offset=base + type_node.offset,
                path=type_node.path,
                track=number,
            )

        uid_node = _find_child(entry.children, ID_TRACK_UID)
        private_node = _find_child(entry.children, ID_CODEC_PRIVATE)
        duration_node = _find_child(entry.children, ID_DEFAULT_DURATION)
        lacing_node = _find_child(entry.children, ID_FLAG_LACING)
        name_node = _find_child(entry.children, ID_NAME)
        lang_node = _find_child(entry.children, ID_LANGUAGE)

        track = Track(
            number=number,
            uid=_uint_field(data, uid_node, base) if uid_node else None,
            kind=track_type,
            track_type=TRACK_TYPE_NAMES[track_type],
            codec_id=codec_id,
            codec_private=(
                data[private_node.data_offset : private_node.end_offset] if private_node else b""
            ),
            default_duration_ns=(
                _uint_field(data, duration_node, base) if duration_node else None
            ),
            flag_lacing=bool(_uint_field(data, lacing_node, base)) if lacing_node else True,
            name=decode_string(data, name_node.data_offset, name_node.size) if name_node else None,
            language=decode_string(data, lang_node.data_offset, lang_node.size) if lang_node else None,
            entry_offset=base + entry.offset,
            path=entry.path,
        )
        audio = _find_child(entry.children, ID_AUDIO)
        if audio is not None:
            freq = _find_child(audio.children, ID_SAMPLING_FREQUENCY)
            chans = _find_child(audio.children, ID_CHANNELS)
            track.sampling_frequency = (
                decode_float(data, freq.data_offset, freq.size, base=base, path=freq.path)
                if freq
                else None
            )
            track.channels = _uint_field(data, chans, base) if chans else None
        video = _find_child(entry.children, ID_VIDEO)
        if video is not None:
            width = _find_child(video.children, ID_PIXEL_WIDTH)
            height = _find_child(video.children, ID_PIXEL_HEIGHT)
            track.pixel_width = _uint_field(data, width, base) if width else None
            track.pixel_height = _uint_field(data, height, base) if height else None
        tracks.append(track)
    if not tracks:
        raise InputError(
            "Segment contains no TrackEntry elements",
            offset=base + tracks_node.offset,
            path=tracks_node.path,
        )
    return tracks


def _check_codec_tables() -> None:
    from .mp4 import SUPPORTED_MP4_CODECS

    if set(SUPPORTED_CODECS) != set(SUPPORTED_MP4_CODECS):
        raise RuntimeError(
            "codec tables disagree: "
            f"{sorted(set(SUPPORTED_CODECS) ^ set(SUPPORTED_MP4_CODECS))}"
        )
    for codec_id, (_, entry) in SUPPORTED_CODECS.items():
        if SUPPORTED_MP4_CODECS[codec_id] != entry:
            raise RuntimeError(f"codec {codec_id}: sample entry mismatch")


_check_codec_tables()


def parse_block_header(
    data: bytes, start: int, end: int, *, base: int, path: str
) -> tuple[int, int, int, int]:
    """Decode a (Simple)Block header: track number, relative timecode, flags.

    Returns ``(track, relative_timecode, flags, payload_start)``.  The track
    number uses an EBML VINT without the marker bit.
    """
    if start >= end:
        raise EbmlParseError("empty block", offset=base + start, path=path)
    first = data[start]
    if first == 0:
        raise EbmlParseError(
            "invalid block track VINT: leading zero byte", offset=base + start, path=path
        )
    length = 9 - first.bit_length()
    if length > 8:
        raise EbmlParseError(
            "invalid block track VINT length", offset=base + start, path=path
        )
    if start + length > end:
        raise EbmlParseError("truncated block track number", offset=base + start, path=path)
    track = first & (0xFF >> length)
    for byte in data[start + 1 : start + length]:
        track = (track << 8) | byte
    pos = start + length
    if pos + 3 > end:
        raise EbmlParseError(
            "truncated block header: need 3 bytes for timecode and flags",
            offset=base + pos,
            path=path,
        )
    timecode = int.from_bytes(data[pos : pos + 2], "big", signed=True)
    flags = data[pos + 2]
    return track, timecode, flags, pos + 3


def lace_frame_sizes(
    data: bytes, start: int, end: int, lacing: int, *, base: int, path: str
) -> tuple[list[int], int]:
    """Split a laced payload into frame sizes.

    Returns ``(frame_sizes, payload_offset)`` where ``payload_offset`` is the
    first byte after the lace header.  Frame sizes sum to ``end - offset``.
    Only the lacing modes Matroska defines are supported; malformed lace
    headers are rejected with their byte offset.  Laced blocks are expanded
    into one logical sample per frame by the caller.
    """
    payload_len = end - start
    if lacing == 0:
        return [payload_len], start
    if payload_len < 1:
        raise EbmlParseError("laced block has no lace header", offset=base + start, path=path)
    frame_count = data[start] + 1
    pos = start + 1
    sizes: list[int] = []
    if lacing == 1:  # Xiph: n-1 sizes, each a chain of 255s terminated < 255
        for _ in range(frame_count - 1):
            size = 0
            while True:
                if pos >= end:
                    raise EbmlParseError("truncated Xiph lace", offset=base + pos, path=path)
                byte = data[pos]
                size += byte
                pos += 1
                if byte != 255:
                    break
            sizes.append(size)
    elif lacing == 3:  # EBML: unsigned first size, signed VINT deltas after
        first_size, length, unknown = read_vint_size(data, pos, base=base, path=path)
        if unknown or first_size is None:
            raise EbmlParseError("invalid EBML lace size", offset=base + pos, path=path)
        sizes.append(first_size)
        pos += length
        for _ in range(frame_count - 2):
            raw, length, bad = read_vint_size(data, pos, base=base, path=path)
            if bad or raw is None:
                raise EbmlParseError("invalid EBML lace delta", offset=base + pos, path=path)
            bias = (1 << (7 * length - 1)) - 1
            sizes.append(sizes[-1] + (raw - bias))
            pos += length
    elif lacing == 2:  # fixed-size: equal frames, no size list
        remaining = payload_len - 1
        if frame_count <= 0 or remaining < 0 or remaining % frame_count:
            raise EbmlParseError(
                f"fixed-size lace of {frame_count} frames does not divide {remaining} bytes",
                offset=base + start,
                path=path,
            )
        frame = remaining // frame_count
        return [frame] * frame_count, start + 1
    else:
        raise EbmlParseError(
            f"unsupported lacing mode {lacing}", offset=base + start, path=path
        )

    header_bytes = pos - start
    total_known = sum(sizes)
    remaining = payload_len - header_bytes - total_known
    if remaining < 0:
        raise EbmlParseError(
            f"lace sizes overrun payload by {-remaining} bytes",
            offset=base + start,
            path=path,
        )
    sizes.append(remaining)
    return sizes, pos


def _parse_cluster(data: bytes, cluster_node: RawNode, base: int) -> Cluster:
    timestamp = 0
    blocks: list[Block] = []
    ts_seen = False
    for item in cluster_node.children:
        if item.element_id == ID_CLUSTER_TIMESTAMP:
            timestamp = _uint_field(data, item, base)
            ts_seen = True
        elif item.element_id == ID_SIMPLE_BLOCK:
            blocks.extend(
                _parse_block_payload(
                    data, item.data_offset, item.end_offset, base, item.path,
                    base + cluster_node.offset,
                )
            )
        elif item.element_id == ID_BLOCK_GROUP:
            block_node = _find_child(item.children, ID_BLOCK)
            if block_node is None:
                raise InputError(
                    "BlockGroup without a Block element",
                    offset=base + item.offset,
                    path=item.path,
                )
            duration_node = _find_child(item.children, ID_BLOCK_DURATION)
            duration = _uint_field(data, duration_node, base) if duration_node else None
            frames = _parse_block_payload(
                data, block_node.data_offset, block_node.end_offset, base, block_node.path,
                base + cluster_node.offset,
            )
            if duration is not None and len(frames) == 1:
                frames[0].block_duration = duration
            blocks.extend(frames)
    if not ts_seen:
        # Matroska requires a cluster timestamp; treating a missing one as 0
        # would silently shift every frame, so refuse instead.
        raise InputError(
            "Cluster has no Timestamp element",
            offset=base + cluster_node.offset,
            path=cluster_node.path,
        )
    return Cluster(
        offset=base + cluster_node.offset,
        end_offset=base + cluster_node.end_offset,
        timestamp=timestamp,
        blocks=blocks,
        unknown_size=cluster_node.unknown_size,
    )


def _parse_block_payload(
    data: bytes, start: int, end: int, base: int, path: str, cluster_offset: int
) -> list[Block]:
    """Split one (Simple)Block into logical frames, honouring lacing."""
    track, timecode, flags, payload_start = parse_block_header(
        data, start, end, base=base, path=path
    )
    lacing = (flags >> 1) & 0x03
    if lacing:
        sizes, frame_start = lace_frame_sizes(
            data, payload_start, end, lacing, base=base, path=path
        )
    else:
        sizes, frame_start = [end - payload_start], payload_start
    frames: list[Block] = []
    offset = frame_start
    for index, size in enumerate(sizes):
        frames.append(
            Block(
                track=track,
                data_offset=base + offset,
                data_length=size,
                timecode=timecode,
                flags=flags,
                cluster_offset=cluster_offset,
                lace_index=index,
                lace_count=len(sizes),
            )
        )
        offset += size
    return frames
