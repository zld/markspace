"""Low-level container structure readers: ISO base media (MP4) boxes and
Matroska EBML elements.

Only structural metadata is parsed.  All reads are bounded by explicit
element ranges so a corrupt header cannot trigger unbounded allocation,
recursion is capped, and no payload bytes are ever decoded as code.
"""

from __future__ import annotations

import struct
from dataclasses import dataclass

# A single media file must not exceed 2 GiB and parsing must stay well below
# the 64 MiB memory budget; reads are therefore small and strictly bounded.
MAX_FILE_SIZE = 2 * 1024 * 1024 * 1024
MAX_BOX_DEPTH = 16
_READ = 8


class ContainerError(Exception):
    """Raised on malformed container structure (bad header, out-of-range
    element, integer overflow, excessive nesting, ...)."""


@dataclass(slots=True)
class Box:
    """One MP4 box located inside an open binary file."""

    f: object
    kind: bytes
    start: int          # offset of the size field
    header: int         # offset of the first payload byte
    end: int            # exclusive end offset

    def payload(self) -> bytes:
        """Read the full box payload.  Callers only invoke this on small
        metadata boxes, never on media data boxes."""
        length = self.end - self.header
        if length < 0 or length > 64 * 1024 * 1024:
            raise ContainerError(f"box {self.kind!r} payload out of bounds")
        self.f.seek(self.header)
        data = self.f.read(length)
        if len(data) != length:
            raise ContainerError(f"truncated box {self.kind!r} payload")
        return data


# Boxes whose payload is itself a sequence of child boxes.
_CONTAINER_BOXES = {
    b"moov", b"trak", b"mdia", b"minf", b"stbl", b"edts",
    b"mvex", b"moof", b"traf", b"mfra", b"udta", b"dinf",
}
# 'meta' is a FullBox (version+flags prefix) followed by child boxes.
_FULL_CONTAINER_BOXES = {b"meta"}


def iter_boxes(f, start: int, end: int, depth: int = 0):
    """Yield every Box strictly inside the [start, end) range."""
    if depth > MAX_BOX_DEPTH:
        raise ContainerError("box nesting deeper than 16 levels")
    pos = start
    while pos < end:
        if end - pos < 8:
            raise ContainerError("truncated box header")
        f.seek(pos)
        header = f.read(8)
        if len(header) != 8:
            raise ContainerError("truncated box header")
        size32, kind = struct.unpack(">I4s", header)
        payload_start = pos + 8
        if size32 == 1:  # 64-bit extended size
            ext = f.read(8)
            if len(ext) != 8:
                raise ContainerError("truncated 64-bit box size")
            size = struct.unpack(">Q", ext)[0]
            payload_start += 8
        elif size32 == 0:  # box extends to end of enclosing range/file
            size = end - pos
        else:
            size = size32
        if size < payload_start - pos or size > MAX_FILE_SIZE:
            raise ContainerError(f"invalid box size for {kind!r}")
        box_end = pos + size
        if box_end > end:
            raise ContainerError(f"box {kind!r} extends past enclosing range")
        box = Box(f, kind, pos, payload_start, box_end)
        yield box
        if kind in _CONTAINER_BOXES:
            yield from iter_boxes(f, payload_start, box_end, depth + 1)
        elif kind in _FULL_CONTAINER_BOXES:
            if box_end - payload_start < 4:
                raise ContainerError("truncated full-box header")
            yield from iter_boxes(f, payload_start + 4, box_end, depth + 1)
        pos = box_end


def find_boxes(f, start: int, end: int, wanted: bytes):
    """Direct children (any nesting depth reached via iter_boxes) of a
    given kind inside [start, end)."""
    return [b for b in iter_boxes(f, start, end) if b.kind == wanted]


def _u16(data: bytes, off: int) -> int:
    if off + 2 > len(data):
        raise ContainerError("field read past box payload")
    return struct.unpack_from(">H", data, off)[0]


def _u32(data: bytes, off: int) -> int:
    if off + 4 > len(data):
        raise ContainerError("field read past box payload")
    return struct.unpack_from(">I", data, off)[0]


def _parse_mdhd(payload: bytes) -> tuple[int | None, int | None]:
    if len(payload) < 4:
        raise ContainerError("truncated mdhd")
    version = payload[0]
    if version == 1:
        if len(payload) < 32:
            raise ContainerError("truncated mdhd v1")
        timescale = _u32(payload, 20)
        duration = struct.unpack_from(">Q", payload, 24)[0]
    else:
        if len(payload) < 20:
            raise ContainerError("truncated mdhd v0")
        timescale = _u32(payload, 12)
        duration = _u32(payload, 16)
    return timescale, duration


def _parse_tkhd(payload: bytes) -> int | None:
    if len(payload) < 4:
        raise ContainerError("truncated tkhd")
    version = payload[0]
    if version not in (0, 1):
        raise ContainerError("unsupported tkhd version")
    # v0: creation(4)+modification(4) then track_ID at offset 12;
    # v1: two 8-byte timestamps then track_ID at offset 20.
    off = 12 if version == 0 else 20
    if len(payload) < off + 4:
        raise ContainerError("truncated tkhd track id")
    return _u32(payload, off)


_HANDLER_TYPES = {b"vide": "video", b"soun": "audio", b"subt": "subtitle",
                  b"text": "subtitle"}


def _parse_hdlr(payload: bytes) -> str | None:
    if len(payload) < 12:
        raise ContainerError("truncated hdlr")
    handler = payload[8:12]
    return _HANDLER_TYPES.get(handler)


def _parse_stsd(payload: bytes, track_type: str | None) -> dict | None:
    if len(payload) < 8:
        raise ContainerError("truncated stsd")
    entry_count = _u32(payload, 4)
    if entry_count == 0:
        return None
    if entry_count != 1 or len(payload) < 16:
        raise ContainerError("unsupported stsd entry layout")
    size = _u32(payload, 8)
    if size < 8 or 8 + size > len(payload):
        raise ContainerError("stsd entry out of bounds")
    codec = payload[12:16]
    body = payload[16:8 + size]
    info: dict = {"codec": codec.decode("latin-1")}
    # SampleEntry layouts are codec-specific; only read the offsets that
    # belong to the handler's type so video entries never gain audio
    # fields or vice versa.  Missing fields stay null, never zero.
    if track_type == "video" and len(body) >= 28:
        # 6 reserved + 2 data-ref-index + 16 pre-defined -> w/h at 24/26.
        info["width"] = _u16(body, 24)
        info["height"] = _u16(body, 26)
    elif track_type == "audio" and len(body) >= 28:
        # AudioSampleEntry: channels at offset 16; sample rate is a 16.16
        # fixed-point value at offset 24, keep only its integer part.
        info["channels"] = _u16(body, 16)
        info["sample_rate"] = _u16(body, 24)
    return info


def parse_mp4(f, file_size: int) -> dict:
    """Parse an open MP4 file and return raw, codec-neutral metadata."""
    if file_size > MAX_FILE_SIZE:
        raise ContainerError("file exceeds 2 GiB limit")
    f.seek(0)
    head = f.read(16)
    if len(head) < 12 or not (head[4:8] == b"ftyp"
                              or head[8:12] == b"ftyp"):
        raise ContainerError("missing ftyp box")
    result: dict = {"container": "mp4", "duration_ms": None, "tracks": []}
    f.seek(0)
    for box in iter_boxes(f, 0, file_size):
        if box.kind != b"moov":
            continue
        for mvhd in find_boxes(f, box.header, box.end, b"mvhd"):
            payload = mvhd.payload()
            if len(payload) >= 20 and payload[0] == 0:
                timescale = _u32(payload, 12)
                duration = _u32(payload, 16)
                if timescale:
                    result["duration_ms"] = round(duration * 1000 / timescale)
        for trak in find_boxes(f, box.header, box.end, b"trak"):
            track: dict = {"track_id": None, "type": None, "codec": None,
                           "duration_ms": None, "width": None,
                           "height": None, "sample_rate": None,
                           "channels": None}
            for tkhd in find_boxes(f, trak.header, trak.end, b"tkhd"):
                track["track_id"] = _parse_tkhd(tkhd.payload())
            for mdia in find_boxes(f, trak.header, trak.end, b"mdia"):
                for mdhd in find_boxes(f, mdia.header, mdia.end, b"mdhd"):
                    timescale, duration = _parse_mdhd(mdhd.payload())
                    if timescale and duration is not None:
                        track["duration_ms"] = round(duration * 1000 / timescale)
                for hdlr in find_boxes(f, mdia.header, mdia.end, b"hdlr"):
                    track["type"] = _parse_hdlr(hdlr.payload())
                for stsd in find_boxes(f, mdia.header, mdia.end, b"stsd"):
                    entry = _parse_stsd(stsd.payload(), track["type"])
                    if entry:
                        track.update(entry)
            result["tracks"].append(track)
        break  # only the first moov is authoritative
    return result


# ---------------------------------------------------------------------------
# Matroska / EBML
# ---------------------------------------------------------------------------

# Element IDs actually needed for metadata extraction (full EBML spec has
# hundreds; unknown elements are skipped structurally, never interpreted).
EBML_HEADER = 0x1A45DFA3
EBML_SEGMENT = 0x18538067
EBML_INFO = 0x1549A966
EBML_TRACKS = 0x1654AE6B
EBML_TRACK_ENTRY = 0xAE
EBML_TRACK_NUMBER = 0xD7
EBML_TRACK_TYPE = 0x83
EBML_CODEC_ID = 0x86
EBML_VIDEO = 0xE0
EBML_AUDIO = 0xE1
EBML_PIXEL_WIDTH = 0xB0
EBML_PIXEL_HEIGHT = 0xBA
EBML_SAMPLING_FREQ = 0xB5
EBML_CHANNELS = 0x9F
EBML_TIMECODE_SCALE = 0x2AD7B1
EBML_DURATION = 0x4489
EBML_VOID = 0xEC
EBML_CRC32 = 0xBF

_MKV_TRACK_TYPES = {1: "video", 2: "audio", 17: "subtitle"}


@dataclass(slots=True)
class EBMLElement:
    """One Matroska EBML element located inside an open binary file."""

    f: object
    eid: int
    start: int
    header: int
    end: int

    def payload(self) -> bytes:
        length = self.end - self.header
        if length < 0 or length > 64 * 1024 * 1024:
            raise ContainerError("EBML payload out of bounds")
        self.f.seek(self.header)
        data = self.f.read(length)
        if len(data) != length:
            raise ContainerError("truncated EBML payload")
        return data


def read_vint(f, pos: int, end: int) -> tuple[int, int, bool]:
    """Read an EBML variable-length *size* integer.

    Returns (value_without_marker, position_after, unknown_marker).
    Raises ContainerError on truncated input or an invalid leading byte.
    """
    if pos >= end:
        raise ContainerError("truncated VINT")
    f.seek(pos)
    first = f.read(1)
    if not first:
        raise ContainerError("truncated VINT")
    lead = first[0]
    if lead == 0:
        raise ContainerError("invalid VINT leading byte")
    length = 9 - lead.bit_length()  # leading zero count + 1
    if length > 8 or pos + length > end:
        raise ContainerError("incomplete VINT")
    rest = f.read(length - 1)
    if len(rest) != length - 1:
        raise ContainerError("incomplete VINT")
    data_mask = 0xFF >> length
    value = lead & data_mask
    unknown = value == data_mask and all(b == 0xFF for b in rest)
    for byte in rest:
        value = (value << 8) | byte
    return value, pos + length, unknown


def read_ebml_id(f, pos: int, end: int) -> tuple[int, int]:
    """Read an EBML element ID: unlike size VINTs the marker bits are part
    of the identifier, so the raw byte sequence is reassembled."""
    if pos >= end:
        raise ContainerError("truncated EBML id")
    f.seek(pos)
    first = f.read(1)
    if not first:
        raise ContainerError("truncated EBML id")
    lead = first[0]
    if lead == 0:
        raise ContainerError("invalid EBML id leading byte")
    length = 9 - lead.bit_length()
    if length > 4 or pos + length > end:
        raise ContainerError("incomplete EBML id")
    rest = f.read(length - 1)
    if len(rest) != length - 1:
        raise ContainerError("incomplete EBML id")
    eid = lead
    for byte in rest:
        eid = (eid << 8) | byte
    return eid, pos + length


def iter_ebml(f, start: int, end: int, depth: int = 0):
    """Yield direct-child EBMLElements inside [start, end)."""
    if depth > MAX_BOX_DEPTH:
        raise ContainerError("EBML nesting deeper than 16 levels")
    pos = start
    while pos < end:
        eid, after_id = read_ebml_id(f, pos, end)
        size, payload_start, unknown_size = read_vint(f, after_id, end)
        if unknown_size:
            elem_end = end  # element runs to end of parent range
        else:
            if size > MAX_FILE_SIZE:
                raise ContainerError("EBML element size overflow")
            elem_end = payload_start + size
            if elem_end > end:
                raise ContainerError("EBML element extends past parent")
        yield EBMLElement(f, eid, pos, payload_start, elem_end)
        pos = elem_end


def _ebml_uint(payload: bytes) -> int:
    return int.from_bytes(payload, "big") if payload else 0


def _ebml_float(payload: bytes) -> float | None:
    if len(payload) == 4:
        return struct.unpack(">f", payload)[0]
    if len(payload) == 8:
        return struct.unpack(">d", payload)[0]
    raise ContainerError("unsupported EBML float width")


def _empty_mkv_track() -> dict:
    return {"track_id": None, "type": None, "codec": None,
            "duration_ms": None, "width": None, "height": None,
            "sample_rate": None, "channels": None}


def parse_matroska(f, file_size: int) -> dict:
    """Parse an open Matroska/WebM file and return raw metadata in the
    same shape as :func:`parse_mp4`."""
    if file_size > MAX_FILE_SIZE:
        raise ContainerError("file exceeds 2 GiB limit")
    top = list(iter_ebml(f, 0, file_size))
    if not top or top[0].eid != EBML_HEADER:
        raise ContainerError("missing EBML header")
    segments = [el for el in top if el.eid == EBML_SEGMENT]
    if not segments:
        raise ContainerError("missing EBML segment")
    segment = segments[0]

    timescale_ns = 1_000_000
    duration_raw: float | None = None
    tracks: list[dict] = []

    for child in iter_ebml(f, segment.header, segment.end):
        if child.eid == EBML_INFO:
            for field in iter_ebml(f, child.header, child.end):
                if field.eid == EBML_TIMECODE_SCALE:
                    timescale_ns = _ebml_uint(field.payload())
                elif field.eid == EBML_DURATION:
                    duration_raw = _ebml_float(field.payload())
        elif child.eid == EBML_TRACKS:
            for entry in iter_ebml(f, child.header, child.end, 1):
                if entry.eid != EBML_TRACK_ENTRY:
                    continue
                track = _empty_mkv_track()
                for field in iter_ebml(f, entry.header, entry.end, 2):
                    if field.eid == EBML_TRACK_NUMBER:
                        track["track_id"] = _ebml_uint(field.payload())
                    elif field.eid == EBML_TRACK_TYPE:
                        track["type"] = _MKV_TRACK_TYPES.get(
                            _ebml_uint(field.payload()))
                    elif field.eid == EBML_CODEC_ID:
                        track["codec"] = field.payload().decode(
                            "latin-1").rstrip("\x00")
                    elif field.eid == EBML_VIDEO:
                        for vf in iter_ebml(f, field.header, field.end, 3):
                            if vf.eid == EBML_PIXEL_WIDTH:
                                track["width"] = _ebml_uint(vf.payload())
                            elif vf.eid == EBML_PIXEL_HEIGHT:
                                track["height"] = _ebml_uint(vf.payload())
                    elif field.eid == EBML_AUDIO:
                        for af in iter_ebml(f, field.header, field.end, 3):
                            if af.eid == EBML_SAMPLING_FREQ:
                                rate = _ebml_float(af.payload())
                                track["sample_rate"] = (
                                    int(rate) if rate is not None
                                    and float(rate).is_integer() else rate)
                            elif af.eid == EBML_CHANNELS:
                                track["channels"] = _ebml_uint(af.payload())
                tracks.append(track)

    result: dict = {"container": "matroska", "duration_ms": None,
                    "timescale_ns": timescale_ns, "tracks": tracks}
    if duration_raw is not None and timescale_ns:
        result["duration_ms"] = round(duration_raw * timescale_ns / 1_000_000)
        for track in tracks:  # no per-track duration in minimal MKV schema
            track["duration_ms"] = result["duration_ms"]
    return result
