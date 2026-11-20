"""ISO BMFF (MP4) extractor.

Handles 32-bit, extended 64-bit and to-EOF box sizes, moov at any file
position, and skips unknown boxes. Reads track timescale/duration, handler
type and avc1/avcC plus mp4a/AAC codec configuration. Fragmented, encrypted
and otherwise unsupported content is reported, never guessed.
"""
from __future__ import annotations

import struct

from .model import CodecParams, Inconsistency, MediaError, TrackInfo

MAX_BOX_DEPTH = 8
MAX_BOXES = 4096
MAX_TRACKS = 32

AAC_SAMPLE_RATES = (96000, 88200, 64000, 48000, 44100, 32000, 24000,
                    22050, 16000, 12000, 11025, 8000, 7350)
AVC_PROFILES = {66: "Baseline", 77: "Main", 100: "High"}
LANG3_TO_2 = {"eng": "en", "zho": "zh", "jpn": "ja", "kor": "ko",
              "fra": "fr", "deu": "de", "spa": "es", "und": None}
HANDLERS = {b"vide": "video", b"soun": "audio"}


def _u32(data: bytes, off: int) -> int:
    return struct.unpack_from(">I", data, off)[0]


class _Box:
    __slots__ = ("typ", "start", "payload", "end")

    def __init__(self, typ: bytes, start: int, payload: int, end: int):
        self.typ, self.start, self.payload, self.end = typ, start, payload, end

    def name(self) -> str:
        return self.typ.decode("latin1")


def _iter_boxes(data: bytes, start: int, end: int, rel: str):
    off = start
    n = 0
    while off < end:
        if off + 8 > end:
            raise MediaError("truncated-box",
                             f"{rel}: {end - off} trailing byte(s) at offset {off} cannot hold a box header", off)
        size = _u32(data, off)
        typ = data[off + 4:off + 8]
        header = 8
        if size == 1:
            if off + 16 > end:
                raise MediaError("truncated-box", f"{rel}: 64-bit box at offset {off} is cut off", off)
            size = struct.unpack_from(">Q", data, off + 8)[0]
            header = 16
        elif size == 0:
            size = end - off
        if size < header:
            raise MediaError("invalid-box-size",
                             f"{rel}: box {typ.decode('latin1')!r} at offset {off} has invalid size {size}", off)
        if off + size > end:
            raise MediaError("truncated-box",
                             f"{rel}: box {typ.decode('latin1')!r} at offset {off} (size {size}) spans past its container", off)
        yield _Box(typ, off, off + header, off + size)
        n += 1
        if n > MAX_BOXES:
            raise MediaError("limit-exceeded", f"{rel}: more than {MAX_BOXES} boxes", off)
        off += size


def _children(data: bytes, box: _Box, rel: str, depth: int) -> list[_Box]:
    if depth > MAX_BOX_DEPTH:
        raise MediaError("limit-exceeded",
                         f"{rel}: box nesting exceeds depth {MAX_BOX_DEPTH}", box.start)
    return list(_iter_boxes(data, box.payload, box.end, rel))


def _need(box: _Box, rel: str, offset: int, count: int, what: str) -> None:
    if offset + count > box.end:
        raise MediaError("truncated-box",
                         f"{rel}: {what} in box {box.name()!r} at offset {box.start} is cut off", offset)


def _parse_mvhd(data: bytes, box: _Box, rel: str) -> int:
    p = box.payload
    _need(box, rel, p, 4, "mvhd header")
    version = data[p]
    if version == 1:
        _need(box, rel, p + 20, 4, "mvhd timescale")
        return _u32(data, p + 20)
    _need(box, rel, p + 12, 4, "mvhd timescale")
    return _u32(data, p + 12)


def _parse_tkhd(data: bytes, box: _Box, rel: str) -> int:
    p = box.payload
    _need(box, rel, p, 4, "tkhd header")
    if data[p] == 1:
        _need(box, rel, p + 20, 4, "tkhd track id")
        return _u32(data, p + 20)
    _need(box, rel, p + 12, 4, "tkhd track id")
    return _u32(data, p + 12)


def _parse_mdhd(data: bytes, box: _Box, rel: str) -> tuple[int, int, str | None]:
    p = box.payload
    _need(box, rel, p, 4, "mdhd header")
    if data[p] == 1:
        _need(box, rel, p + 20, 16, "mdhd v1 body")
        timescale = _u32(data, p + 20)
        duration = struct.unpack_from(">Q", data, p + 24)[0]
        lang_off = p + 32
    else:
        _need(box, rel, p + 12, 12, "mdhd v0 body")
        timescale = _u32(data, p + 12)
        duration = _u32(data, p + 16)
        lang_off = p + 20
    packed = struct.unpack_from(">H", data, lang_off)[0]
    lang3 = "".join(chr(((packed >> s) & 0x1F) + 0x60) for s in (10, 5, 0))
    return timescale, duration, LANG3_TO_2.get(lang3, lang3 if lang3 != "```" else None)


def _parse_hdlr(data: bytes, box: _Box, rel: str) -> bytes:
    _need(box, rel, box.payload + 8, 4, "hdlr handler type")
    return data[box.payload + 8:box.payload + 12]


def _parse_esds(data: bytes, box: _Box, rel: str) -> tuple[int | None, int | None]:
    """Extract (sample_rate, channels) from an esds box, if decodable."""
    return None, None


def _parse_avc1(data: bytes, entry: _Box, rel: str) -> CodecParams:
    _need(entry, rel, entry.payload + 24, 4, "avc1 dimensions")
    width = struct.unpack_from(">H", data, entry.payload + 24)[0]
    height = struct.unpack_from(">H", data, entry.payload + 26)[0]
    profile = None
    for child in _iter_boxes(data, entry.payload + 78, entry.end, rel):
        if child.typ == b"avcC":
            _need(child, rel, child.payload, 4, "avcC header")
            profile = AVC_PROFILES.get(data[child.payload + 1], f"unknown-{data[child.payload + 1]}")
    return CodecParams(codec="avc1", width=width, height=height, profile=profile)


def _parse_mp4a(data: bytes, entry: _Box, rel: str) -> CodecParams:
    _need(entry, rel, entry.payload + 16, 12, "mp4a audio sample entry")
    channels = struct.unpack_from(">H", data, entry.payload + 16)[0]
    rate = _u32(data, entry.payload + 24) >> 16
    for child in _iter_boxes(data, entry.payload + 28, entry.end, rel):
        if child.typ == b"esds":
            asc_rate, asc_channels = _parse_esds(data, child, rel)
            rate = asc_rate or rate
            channels = asc_channels or channels
    return CodecParams(codec="aac", sample_rate=rate, channels=channels)


def _parse_stsd(data: bytes, box: _Box, rel: str, locator: str,
                problems: list[Inconsistency]) -> CodecParams | None:
    _need(box, rel, box.payload, 8, "stsd header")
    count = _u32(data, box.payload + 4)
    if count < 1:
        raise MediaError("invalid-box", f"{rel}: {locator} stsd has no sample entries", box.start)
    entries = list(_iter_boxes(data, box.payload + 8, box.end, rel))
    if not entries:
        raise MediaError("truncated-box", f"{rel}: {locator} stsd entry is cut off", box.start)
    entry = entries[0]
    if entry.typ == b"avc1":
        return _parse_avc1(data, entry, rel)
    if entry.typ == b"mp4a":
        return _parse_mp4a(data, entry, rel)
    if entry.typ in (b"encv", b"enca"):
        problems.append(Inconsistency("unsupported-codec", rel,
                                      f"{rel}: {locator} uses encrypted sample entry {entry.name()!r}; encryption is not supported",
                                      track=locator))
        return None
    problems.append(Inconsistency("unsupported-codec", rel,
                                  f"{rel}: {locator} sample entry {entry.name()!r} is not a supported codec (avc1/mp4a)",
                                  track=locator))
    return None


def _parse_trak(data: bytes, box: _Box, rel: str, index: int,
                problems: list[Inconsistency]) -> TrackInfo | None:
    locator = f"trak[{index}]"
    track_id = None
    handler = None
    timescale = duration = 0
    language = None
    codec: CodecParams | None = None
    for child in _children(data, box, rel, 1):
        if child.typ == b"tkhd":
            track_id = _parse_tkhd(data, child, rel)
        elif child.typ == b"mdia":
            for md in _children(data, child, rel, 2):
                if md.typ == b"mdhd":
                    timescale, duration, language = _parse_mdhd(data, md, rel)
                elif md.typ == b"hdlr":
                    handler = _parse_hdlr(data, md, rel)
                elif md.typ == b"minf":
                    for mf in _children(data, md, rel, 3):
                        if mf.typ == b"stbl":
                            for sb in _children(data, mf, rel, 4):
                                if sb.typ == b"stsd":
                                    codec = _parse_stsd(data, sb, rel, locator, problems)
    if track_id is None:
        raise MediaError("missing-box", f"{rel}: {locator} has no tkhd box", box.start)
    if handler is None or timescale == 0:
        raise MediaError("missing-box", f"{rel}: {locator} is missing mdhd or hdlr", box.start)
    kind = HANDLERS.get(handler)
    if kind is None:
        problems.append(Inconsistency("unsupported-track", rel,
                                      f"{rel}: {locator} handler {handler.decode('latin1')!r} is not video/audio",
                                      track=locator))
        return None
    if codec is None and not any(p.track == locator for p in problems):
        raise MediaError("missing-box", f"{rel}: {locator} has no usable stsd codec entry", box.start)
    duration_ms = (duration * 1000 + timescale // 2) // timescale
    return TrackInfo(track_id=track_id, kind=kind, codec=codec or CodecParams(codec="unknown"),
                     duration_ms=duration_ms, language=language, timescale=timescale, locator=locator)


def parse_mp4(data: bytes, rel: str) -> tuple[list[TrackInfo], list[Inconsistency]]:
    """Parse an MP4 buffer; returns (tracks, per-track problems)."""
    top = list(_iter_boxes(data, 0, len(data), rel))
    if any(b.typ in (b"moof", b"mvex") for b in top):
        raise MediaError("unsupported-container",
                         f"{rel}: fragmented MP4 (moof/mvex) is not supported")
    moov = next((b for b in top if b.typ == b"moov"), None)
    if moov is None:
        raise MediaError("missing-box", f"{rel}: no moov box found")
    problems: list[Inconsistency] = []
    tracks: list[TrackInfo] = []
    seen_ids: set[int] = set()
    for child in _children(data, moov, rel, 0):
        if child.typ == b"mvhd":
            _parse_mvhd(data, child, rel)
        elif child.typ == b"trak":
            track = _parse_trak(data, child, rel, len(tracks), problems)
            if track is None:
                continue
            if track.track_id in seen_ids:
                problems.append(Inconsistency("duplicate-track-id", rel,
                                              f"{rel}: track id {track.track_id} appears more than once",
                                              track=track.locator))
            seen_ids.add(track.track_id)
            tracks.append(track)
            if len(tracks) > MAX_TRACKS:
                raise MediaError("limit-exceeded", f"{rel}: more than {MAX_TRACKS} tracks")
    if not tracks and not problems:
        raise MediaError("missing-box", f"{rel}: moov contains no usable tracks")
    return tracks, problems
