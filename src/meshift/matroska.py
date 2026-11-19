"""Matroska reading layer built on top of the generic EBML parser."""

from __future__ import annotations

from dataclasses import dataclass, field

from . import ebml
from .errors import (EBMLError, MatroskaError, TruncatedInputError,
                     UnsupportedLacingError)

# Matroska TrackType enumeration.
TRACK_VIDEO = 1
TRACK_AUDIO = 2
TRACK_SUBTITLE = 17

DEFAULT_TIMECODE_SCALE = 1_000_000  # nanoseconds per Timecode tick


@dataclass
class Track:
    number: int
    uid: int | None
    kind: int
    codec_id: str
    default_duration_ns: int | None = None

    @property
    def kind_name(self) -> str:
        return {TRACK_VIDEO: "video", TRACK_AUDIO: "audio",
                TRACK_SUBTITLE: "subtitle"}.get(self.kind, f"type{self.kind}")


@dataclass
class RawBlock:
    """One compressed sample with timing still in Matroska units."""

    track_number: int
    relative_timecode: int       # signed int16, Cluster timecode units
    flags: int
    payload: bytes
    composition_offset: int = 0  # already in Track timecode units
    is_keyframe: bool = False
    source: str = "SimpleBlock"
    cluster_timecode: int | None = None
    cluster_index: int = 0
    block_index: int = 0

    @property
    def cluster_ns(self) -> int | None:
        if self.cluster_timecode is None:
            return None
        return self.cluster_timecode * DEFAULT_TIMECODE_SCALE


@dataclass
class MatroskaFile:
    timecode_scale: int
    tracks: list[Track]
    blocks: list[RawBlock] = field(default_factory=list)
    roots: list[ebml.Element] = field(default_factory=list)


def _read_signed_int16(data: bytes, pos: int) -> int:
    if pos + 2 > len(data):
        raise TruncatedInputError("truncated block relative timecode", pos)
    return int.from_bytes(data[pos:pos + 2], "big", signed=True)


def _parse_block_bytes(element: ebml.Element) -> tuple[int, int, int, bytes]:
    data = element.payload
    track_num, width, unknown = ebml.read_vint(data, 0, kind="block track")
    if unknown:
        raise EBMLError("block track number uses reserved VINT", element.start)
    pos = width
    relative = _read_signed_int16(data, pos)
    pos += 2
    if pos >= len(data):
        raise TruncatedInputError("block missing flags octet", element.start)
    flags = data[pos]
    pos += 1
    lacing = (flags >> 1) & 0x03
    if lacing != 0:
        raise UnsupportedLacingError(
            f"lacing mode {lacing} in {element.name} cannot be remuxed "
            "without splitting samples")
    return track_num, relative, flags, data[pos:]


def _parse_track(entry: ebml.Element) -> Track:
    number_el = entry.find(ebml.TRACK_NUMBER)
    type_el = entry.find(ebml.TRACK_TYPE)
    codec_el = entry.find(ebml.CODEC_ID)
    if number_el is None or type_el is None or codec_el is None:
        raise MatroskaError(
            "TrackEntry missing TrackNumber/TrackType/CodecID", )
    track = Track(number=number_el.as_uint(),
                  uid=(entry.find(ebml.TRACK_UID).as_uint()
                       if entry.find(ebml.TRACK_UID) else None),
                  kind=type_el.as_uint(), codec_id=codec_el.as_text())
    default_duration = entry.find(ebml.DEFAULT_DURATION)
    if default_duration is not None:
        track.default_duration_ns = default_duration.as_uint()
    return track


def parse_matroska(data: bytes) -> MatroskaFile:
    """Parse a Matroska byte string into tracks and ordered raw blocks."""
    roots = ebml.parse_tree(data)
    segment = next((r for r in roots if r.element_id == ebml.SEGMENT), None)
    if segment is None:
        raise MatroskaError("no Matroska Segment element found")

    info = segment.find(ebml.INFO)
    scale = DEFAULT_TIMECODE_SCALE
    if info is not None:
        scale_el = info.find(ebml.TIMECODE_SCALE)
        if scale_el is not None:
            scale = scale_el.as_uint()
    if scale <= 0:
        raise MatroskaError(f"invalid TimecodeScale {scale}")

    tracks_el = segment.find(ebml.TRACKS)
    if tracks_el is None:
        raise MatroskaError("Segment contains no Tracks element")
    tracks = [_parse_track(entry)
              for entry in tracks_el.find_all(ebml.TRACK_ENTRY)]
    track_numbers = {track.number for track in tracks}

    blocks: list[RawBlock] = []
    cluster_index = 0
    for cluster in segment.find_all(ebml.CLUSTER):
        timecode_el = cluster.find(ebml.TIMECODE)
        cluster_tc = timecode_el.as_uint() if timecode_el is not None else None
        block_index = 0
        for child in cluster.children:
            if child.element_id == ebml.SIMPLE_BLOCK:
                num, rel, flags, payload = _parse_block_bytes(child)
                if num not in track_numbers:
                    raise MatroskaError(
                        f"block references unknown track number {num}")
                keyframe = bool(flags & 0x80)
                blocks.append(RawBlock(
                    num, rel, flags, payload, is_keyframe=keyframe,
                    source="SimpleBlock", cluster_timecode=cluster_tc,
                    cluster_index=cluster_index, block_index=block_index))
            elif child.element_id == ebml.BLOCK_GROUP:
                block_el = child.find(ebml.BLOCK)
                if block_el is None:
                    raise MatroskaError("BlockGroup without Block")
                num, rel, flags, payload = _parse_block_bytes(block_el)
                if num not in track_numbers:
                    raise MatroskaError(
                        f"block references unknown track number {num}")
                ref = child.find(ebml.REFERENCE_BLOCK)
                offset_el = child.find(ebml.COMPOSITION_OFFSET)
                composition = offset_el.as_sint() if offset_el else 0
                blocks.append(RawBlock(
                    num, rel, flags, payload,
                    composition_offset=composition,
                    is_keyframe=ref is None, source="Block",
                    cluster_timecode=cluster_tc,
                    cluster_index=cluster_index, block_index=block_index))
            block_index += 1
        cluster_index += 1

    return MatroskaFile(scale, tracks, blocks, roots)
