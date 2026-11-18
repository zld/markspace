"""Matroska semantics: Segment, Info, Tracks, Clusters and SimpleBlock."""
from __future__ import annotations

import struct
from dataclasses import dataclass, field

from .ebml import read_size
from .elements import (
    ID_EBML_HEADER, ID_SEGMENT, ID_CLUSTER, ID_SIMPLE_BLOCK, ID_BLOCK,
    Element, parse_file,
)

ID_INFO = 0x1549A966
ID_TIMESTAMP_SCALE = 0x2AD7B1
ID_DURATION = 0x4489
ID_TRACKS = 0x1654AE6B
ID_TRACK_ENTRY = 0xAE
ID_TRACK_NUMBER = 0xD7
ID_TRACK_UID = 0x73C5
ID_TRACK_TYPE = 0x83
ID_FLAG_ENABLED = 0xB9
ID_FLAG_DEFAULT = 0x88
ID_CODEC_ID = 0x86
ID_CODEC_PRIVATE = 0x63A2
ID_LANGUAGE = 0x22B59C
ID_DEFAULT_DURATION = 0x23E383
ID_VIDEO = 0xE0
ID_PIXEL_WIDTH = 0xB0
ID_PIXEL_HEIGHT = 0xBA
ID_AUDIO = 0xE1
ID_SAMPLING_FREQ = 0xB5
ID_CHANNELS = 0x9F
ID_TIMESTAMP = 0xE7
ID_BLOCK_GROUP = 0xA0

TRACK_TYPE_VIDEO = 1
TRACK_TYPE_AUDIO = 2
TRACK_TYPE_SUBTITLE = 17

SUPPORTED_CODECS = {
    "V_MPEG4/ISO/AVC": "avc1",
    "V_MPEGH/ISO/HEVC": "hvc1",
    "A_AAC": "mp4a",
}

DEFAULT_TIMESTAMP_SCALE = 1_000_000  # nanoseconds


class MatroskaError(Exception):
    """Input violates Matroska semantics; ``offset`` locates the fault."""

    def __init__(self, message: str, offset: int = 0):
        super().__init__(f"{message} (at byte offset {offset})")
        self.message = message
        self.offset = offset


@dataclass
class Track:
    number: int
    uid: int = 0
    type: int = 0
    codec_id: str = ""
    codec_private: bytes = b""
    default: bool = True
    enabled: bool = True
    language: str = "und"
    default_duration_ns: int = 0
    width: int = 0
    height: int = 0
    channels: int = 2
    sampling_freq: float = 0.0


@dataclass
class Block:
    track_number: int
    timestamp: int  # absolute, in TimestampScale units
    keyframe: bool
    frames: list[bytes]
    offset: int     # file offset of the block payload (for error reports)


@dataclass
class Segment:
    timestamp_scale: int
    duration: float | None
    tracks: list[Track]
    blocks: list[Block]
    top_elements: list[Element] = field(default_factory=list)

    def track_by_number(self, number: int) -> Track | None:
        for t in self.tracks:
            if t.number == number:
                return t
        return None


def _child(el: Element, eid: int) -> Element | None:
    for c in el.children:
        if c.id == eid:
            return c
    return None


def _payload(data: bytes, el: Element) -> bytes:
    return data[el.data_offset:el.end]


def _uint(data: bytes, el: Element, default: int = 0) -> int:
    p = _payload(data, el)
    return int.from_bytes(p, "big") if p else default


def _float(data: bytes, el: Element) -> float:
    p = _payload(data, el)
    if len(p) == 4:
        return struct.unpack(">f", p)[0]
    return struct.unpack(">d", p)[0]


def _str(data: bytes, el: Element) -> str:
    return _payload(data, el).decode("utf-8", "replace").rstrip("\x00")


def _split_lacing(body: bytes, lacing: int, offset: int) -> list[bytes]:
    if lacing == 0:
        return [body]
    if not body:
        raise MatroskaError("laced block missing frame count", offset)
    nframes = body[0] + 1
    pos = 1
    sizes: list[int] = []
    if lacing == 2:  # fixed-size lacing
        rest = len(body) - 1
        if nframes < 2 or rest % nframes:
            raise MatroskaError("corrupt fixed-size lacing", offset)
        step = rest // nframes
        return [body[1 + i * step:1 + (i + 1) * step] for i in range(nframes)]
    if lacing == 1:  # Xiph lacing
        for _ in range(nframes - 1):
            run = 0
            while True:
                if pos >= len(body):
                    raise MatroskaError("truncated Xiph lacing sizes", offset)
                b = body[pos]
                pos += 1
                run += b
                if b != 255:
                    break
            sizes.append(run)
    else:  # EBML lacing
        prev, w = read_size(body, pos, allow_unknown=False)
        pos += w
        sizes.append(prev)
        for _ in range(nframes - 2):
            raw, w = read_size(body, pos, allow_unknown=False)
            pos += w
            prev = prev + raw - ((1 << (7 * w - 1)) - 1)  # signed vint diff
            if prev < 0:
                raise MatroskaError("negative EBML lacing size", offset)
            sizes.append(prev)
    frames = []
    for size in sizes:
        if pos + size > len(body):
            raise MatroskaError("laced frame extends past block end", offset)
        frames.append(body[pos:pos + size])
        pos += size
    frames.append(body[pos:])  # final frame takes the remainder
    return frames


def parse_block(payload: bytes, cluster_ts: int, offset: int) -> Block:
    """Parse a SimpleBlock/Block payload into individual frames."""
    if len(payload) < 4:
        raise MatroskaError("block too short for header", offset)
    track, w = read_size(payload, 0, allow_unknown=False)
    if len(payload) < w + 3:
        raise MatroskaError("truncated block header", offset)
    rel = int.from_bytes(payload[w:w + 2], "big", signed=True)
    flags = payload[w + 2]
    frames = _split_lacing(payload[w + 3:], (flags >> 1) & 0x03, offset)
    if not frames or any(len(f) == 0 for f in frames):
        raise MatroskaError("block contains an empty frame", offset)
    return Block(track, cluster_ts + rel, bool(flags & 0x80), frames, offset)


def _parse_track(data: bytes, el: Element) -> Track:
    t = Track(number=0)
    for c in el.children:
        if c.id == ID_TRACK_NUMBER:
            t.number = _uint(data, c)
        elif c.id == ID_TRACK_UID:
            t.uid = _uint(data, c)
        elif c.id == ID_TRACK_TYPE:
            t.type = _uint(data, c)
        elif c.id == ID_FLAG_ENABLED:
            t.enabled = bool(_uint(data, c, 1))
        elif c.id == ID_FLAG_DEFAULT:
            t.default = bool(_uint(data, c, 1))
        elif c.id == ID_CODEC_ID:
            t.codec_id = _str(data, c)
        elif c.id == ID_CODEC_PRIVATE:
            t.codec_private = _payload(data, c)
        elif c.id == ID_LANGUAGE:
            t.language = _str(data, c) or "und"
        elif c.id == ID_DEFAULT_DURATION:
            t.default_duration_ns = _uint(data, c)
        elif c.id == ID_VIDEO:
            w, h = _child(c, ID_PIXEL_WIDTH), _child(c, ID_PIXEL_HEIGHT)
            t.width = _uint(data, w) if w else 0
            t.height = _uint(data, h) if h else 0
        elif c.id == ID_AUDIO:
            f, ch = _child(c, ID_SAMPLING_FREQ), _child(c, ID_CHANNELS)
            t.sampling_freq = _float(data, f) if f else 0.0
            t.channels = _uint(data, ch, 2) if ch else 2
    if not t.number:
        raise MatroskaError("TrackEntry missing TrackNumber", el.offset)
    if not t.codec_id:
        raise MatroskaError("TrackEntry missing CodecID", el.offset)
    return t


def _parse_cluster(data: bytes, el: Element) -> list[Block]:
    ts_el = _child(el, ID_TIMESTAMP)
    cluster_ts = _uint(data, ts_el) if ts_el else 0
    blocks = []
    for c in el.children:
        if c.id == ID_SIMPLE_BLOCK:
            blocks.append(parse_block(_payload(data, c), cluster_ts, c.data_offset))
        elif c.id == ID_BLOCK_GROUP:
            for g in c.children:
                if g.id == ID_BLOCK:
                    blocks.append(parse_block(_payload(data, g), cluster_ts,
                                            g.data_offset))
    return blocks


def parse_segment(data: bytes, tops: list[Element] | None = None) -> Segment:
    """Parse the first Segment of a Matroska file into tracks and blocks."""
    if tops is None:
        tops = parse_file(data)
    if not tops or tops[0].id != ID_EBML_HEADER:
        raise MatroskaError("missing EBML header", 0)
    seg = next((e for e in tops if e.id == ID_SEGMENT), None)
    if seg is None:
        raise MatroskaError("missing Segment element", 0)
    scale, duration, tracks, blocks = DEFAULT_TIMESTAMP_SCALE, None, [], []
    for c in seg.children:
        if c.id == ID_INFO:
            sc, du = _child(c, ID_TIMESTAMP_SCALE), _child(c, ID_DURATION)
            if sc:
                scale = _uint(data, sc)
            if du:
                duration = _float(data, du)
        elif c.id == ID_TRACKS:
            for te in c.children:
                if te.id == ID_TRACK_ENTRY:
                    tracks.append(_parse_track(data, te))
        elif c.id == ID_CLUSTER:
            blocks.extend(_parse_cluster(data, c))
    if scale <= 0:
        raise MatroskaError("invalid TimestampScale", 0)
    if not tracks:
        raise MatroskaError("Segment contains no tracks", 0)
    return Segment(scale, duration, tracks, blocks, tops)
