"""Container sniffing and metadata extraction.

This module deliberately parses only the small container subset used by the
project fixtures.  Extensions are never trusted: dispatch happens on magic
bytes and on the structural headers found at offset zero.

Supported subset
----------------
* EBML: ``EBML`` header with ``DocType``, a ``Segment`` containing ``Info``
  (``TimecodeScale``, ``Duration``) and ``Tracks`` (track number, type,
  codec id, codec delay, dimensions/audio parameters and a project-specific
  ``TrackDuration`` element, id ``0x4D7D``).
* ISO BMFF: ``ftyp`` plus ``moov`` boxes with ``mvhd``, ``trak/tkhd``,
  ``mdia/mdhd``, ``mdia/hdlr``, ``mdia/minf/stbl/stsd`` and an optional
  ``edts/elst`` edit list.

Missing metadata is represented as ``None``; it is never replaced by a
fabricated default.  Malformed or truncated input raises :class:`ProbeError`
carrying the byte offset at which the structure stopped making sense.
"""

from __future__ import annotations

import hashlib
import os
import struct
from dataclasses import dataclass
from typing import Iterator

from .models import UNKNOWN, Container, Track

MAX_FILE_BYTES = 256 * 1024 * 1024

EBML_HEADER = 0x1A45DFA3
EBML_DOCTYPE = 0x4282
EBML_SEGMENT = 0x18538067
EBML_INFO = 0x1549A966
EBML_TIMECODE_SCALE = 0x2AD7B1
EBML_DURATION = 0x4489
EBML_TRACKS = 0x1654AE6B
EBML_TRACK_ENTRY = 0xAE
EBML_TRACK_NUMBER = 0xD7
EBML_TRACK_TYPE = 0x83
EBML_CODEC_ID = 0x86
EBML_CODEC_DELAY = 0x56AA
EBML_PIXEL_WIDTH = 0xB0
EBML_PIXEL_HEIGHT = 0xBA
EBML_SAMPLING_FREQUENCY = 0xB5
EBML_CHANNELS = 0x9F
EBML_TRACK_DURATION = 0x4D7D  # project extension, in TimecodeScale units

EBML_TYPES = {1: "video", 2: "audio", 17: "subtitle"}
EBML_CODECS = {
    "V_MPEG4/ISO/AVC": "h264",
    "A_AAC": "aac",
    "S_TEXT/UTF8": "srt",
}

BMFF_BOX_TYPES = {b"ftyp", b"styp", b"moov", b"mdat", b"free", b"wide"}


class ProbeError(Exception):
    """A malformed or unsupported container with a byte offset."""

    def __init__(self, message: str, offset: int | None = None) -> None:
        super().__init__(message)
        self.message = message
        self.offset = offset


@dataclass(frozen=True)
class _Element:
    element_id: int
    offset: int
    data_start: int
    data_end: int


def _read_element_id(data: bytes, pos: int, limit: int) -> tuple[int, int]:
    if pos >= limit:
        raise ProbeError("truncated element id", pos)
    first = data[pos]
    length = 1
    mask = 0x80
    while length <= 4 and not first & mask:
        length += 1
        mask >>= 1
    if length > 4:
        raise ProbeError("malformed element id", pos)
    if pos + length > limit:
        raise ProbeError("truncated element id", pos)
    return int.from_bytes(data[pos:pos + length], "big"), length


def _read_element_size(data: bytes, pos: int, limit: int) -> tuple[int | None, int]:
    if pos >= limit:
        raise ProbeError("truncated element size", pos)
    first = data[pos]
    if first == 0:
        raise ProbeError("malformed element size (zero first byte)", pos)
    length = 1
    mask = 0x80
    while not first & mask:
        length += 1
        mask >>= 1
    if pos + length > limit:
        raise ProbeError("truncated element size", pos)
    if length > 8:
        raise ProbeError("malformed element size", pos)
    value = int.from_bytes(data[pos:pos + length], "big")
    value &= ~(1 << (7 * length))
    unknown = value == (1 << (7 * length)) - 1
    return (None if unknown else value), length


def _iter_elements(data: bytes, start: int, end: int) -> Iterator[_Element]:
    pos = start
    while pos < end:
        offset = pos
        element_id, id_length = _read_element_id(data, pos, end)
        pos += id_length
        size, size_length = _read_element_size(data, pos, end)
        pos += size_length
        if size is None:
            data_end = end
        else:
            data_end = pos + size
            if data_end > end:
                raise ProbeError(
                    f"element 0x{element_id:X} declares {size} bytes, "
                    f"but only {end - pos} remain",
                    offset,
                )
        yield _Element(element_id, offset, pos, data_end)
        pos = data_end


def _uint(payload: bytes, offset: int) -> int:
    if not payload:
        raise ProbeError("empty unsigned integer", offset)
    if len(payload) > 8:
        raise ProbeError(f"unsigned integer is {len(payload)} bytes (max 8)", offset)
    return int.from_bytes(payload, "big")


def _float(payload: bytes, offset: int) -> float:
    if len(payload) == 4:
        return float(struct.unpack(">f", payload)[0])
    if len(payload) == 8:
        return float(struct.unpack(">d", payload)[0])
    raise ProbeError(f"float is {len(payload)} bytes (expected 4 or 8)", offset)


def _string(payload: bytes) -> str:
    return payload.decode("utf-8", "replace").rstrip("\x00")


def _ticks_to_ms(value: float, timescale: int | None) -> int | None:
    if timescale is None or timescale <= 0:
        return None
    return int(value * 1000.0 / timescale + 0.5)


def _ns_to_ms(value: int) -> int:
    return int(value / 1_000_000 + 0.5)


def _one_shot_ms(units: int | None, timescale: int | None) -> int | None:
    """Convert a unit count at `timescale` to milliseconds without overflow."""
    if units is None or timescale is None or timescale <= 0:
        return None
    return (units * 1000 + timescale // 2) // timescale


def _track_sort_key(track: Track) -> tuple:
    return (
        track.kind,
        track.track_id if track.track_id is not None else 1 << 62,
        track.codec or "",
        track.start_ms if track.start_ms is not None else -1,
    )


def _parse_ebml(data: bytes, path: str) -> Container:
    container = Container(path=path, container=UNKNOWN)
    elements = list(_iter_elements(data, 0, len(data)))
    if not elements or elements[0].element_id != EBML_HEADER:
        raise ProbeError("missing EBML header", 0)
    header = elements[0]
    doc_type = None
    for child in _iter_elements(data, header.data_start, header.data_end):
        if child.element_id == EBML_DOCTYPE:
            doc_type = _string(data[child.data_start:child.data_end]) or None
    container.container = f"ebml:{doc_type}" if doc_type else "ebml"

    segment = next(
        (e for e in elements[1:] if e.element_id == EBML_SEGMENT), None
    )
    if segment is None:
        raise ProbeError("missing EBML segment", header.data_end)

    timecode_scale = None
    duration_ticks = None
    track_entries: list[_Element] = []
    for element in _iter_elements(data, segment.data_start, segment.data_end):
        if element.element_id == EBML_INFO:
            for info in _iter_elements(data, element.data_start, element.data_end):
                if info.element_id == EBML_TIMECODE_SCALE:
                    ns = _uint(data[info.data_start:info.data_end], info.offset)
                    if ns <= 0:
                        raise ProbeError("TimecodeScale must be positive", info.offset)
                    timecode_scale = ns
                elif info.element_id == EBML_DURATION:
                    duration_ticks = _float(
                        data[info.data_start:info.data_end], info.offset
                    )
        elif element.element_id == EBML_TRACKS:
            track_entries.extend(
                e for e in _iter_elements(data, element.data_start, element.data_end)
                if e.element_id == EBML_TRACK_ENTRY
            )

    if timecode_scale is not None:
        container.timescale = 1_000_000_000 // timecode_scale
    if duration_ticks is not None:
        container.duration_ms = _ticks_to_ms(duration_ticks, container.timescale)

    for entry in track_entries:
        track = Track(track_id=None, kind=UNKNOWN, codec=None, timescale=None,
                      start_ms=None, duration_ms=None)
        track_duration_ticks = None
        for child in _iter_elements(data, entry.data_start, entry.data_end):
            payload = data[child.data_start:child.data_end]
            if child.element_id == EBML_TRACK_NUMBER:
                track.track_id = _uint(payload, child.offset)
            elif child.element_id == EBML_TRACK_TYPE:
                number = _uint(payload, child.offset)
                track.kind = EBML_TYPES.get(number, UNKNOWN)
                if number not in EBML_TYPES:
                    track.problems.append(f"unknown track type {number}")
            elif child.element_id == EBML_CODEC_ID:
                raw = _string(payload) or None
                track.codec = EBML_CODECS.get(raw, raw) if raw else None
            elif child.element_id == EBML_CODEC_DELAY:
                track.start_ms = _ns_to_ms(_uint(payload, child.offset))
            elif child.element_id == EBML_TRACK_DURATION:
                track_duration_ticks = _float(payload, child.offset)
            elif child.element_id == EBML_PIXEL_WIDTH:
                track.params["width"] = _uint(payload, child.offset)
            elif child.element_id == EBML_PIXEL_HEIGHT:
                track.params["height"] = _uint(payload, child.offset)
            elif child.element_id == EBML_SAMPLING_FREQUENCY:
                track.params["sample_rate"] = int(_float(payload, child.offset) + 0.5)
            elif child.element_id == EBML_CHANNELS:
                track.params["channels"] = _uint(payload, child.offset)
        track.timescale = container.timescale
        if track_duration_ticks is not None:
            track.duration_ms = _ticks_to_ms(track_duration_ticks, container.timescale)
            if track.duration_ms is None:
                track.problems.append("track duration present but time base unknown")
        container.tracks.append(track)

    _finalise_tracks(container)
    return container


def _finalise_tracks(container: Container) -> None:
    container.tracks.sort(key=_track_sort_key)
    counts: dict[int, int] = {}
    for track in container.tracks:
        if track.track_id is not None:
            counts[track.track_id] = counts.get(track.track_id, 0) + 1
    for track in container.tracks:
        track_id = track.track_id
        if track_id is not None and counts[track_id] > 1:
            track.problems.append(
                f"duplicate-track-number:{track_id} ({counts[track_id]} entries)"
            )
        if (
            track.start_ms is not None
            and track.duration_ms is not None
            and container.duration_ms is not None
            and track.start_ms + track.duration_ms > container.duration_ms
        ):
            track.problems.append(
                "track-duration-overflow:"
                f"{track.start_ms + track.duration_ms}>{container.duration_ms}"
            )


BMFF_KINDS = {b"vide": "video", b"soun": "audio", b"subt": "subtitle",
               b"text": "subtitle", b"sbtl": "subtitle"}
BMFF_CODECS = {b"avc1": "h264", b"avc3": "h264", b"mp4a": "aac", b"tx3g": "srt"}


@dataclass(frozen=True)
class _Box:
    box_type: bytes
    offset: int
    data_start: int
    data_end: int


def _read_box(data: bytes, pos: int, limit: int) -> _Box:
    if pos + 8 > limit:
        raise ProbeError("truncated box header", pos)
    size = int.from_bytes(data[pos:pos + 4], "big")
    box_type = data[pos + 4:pos + 8]
    header = 8
    if size == 1:
        if pos + 16 > limit:
            raise ProbeError("truncated 64-bit box header", pos)
        size = int.from_bytes(data[pos + 8:pos + 16], "big")
        header = 16
    elif size == 0:
        size = limit - pos
    if size < header:
        raise ProbeError(f"box {box_type!r} declares invalid size {size}", pos)
    end = pos + size
    if end > limit:
        raise ProbeError(
            f"box {box_type!r} declares {size} bytes, but only {limit - pos} remain",
            pos,
        )
    return _Box(box_type, pos, pos + header, end)


def _iter_boxes(data: bytes, start: int, end: int) -> Iterator[_Box]:
    pos = start
    while pos < end:
        box = _read_box(data, pos, end)
        yield box
        pos = box.data_end


def _child_boxes(data: bytes, box: _Box) -> list[_Box]:
    return list(_iter_boxes(data, box.data_start, box.data_end))


def _need(body: bytes, length: int, offset: int, label: str) -> None:
    if len(body) < length:
        raise ProbeError(
            f"{label} is truncated ({len(body)} bytes, need {length})", offset
        )


def _parse_mvhd(body: bytes, offset: int) -> tuple[int, int]:
    _need(body, 20, offset, "mvhd v0")
    version = body[0]
    if version == 1:
        _need(body, 32, offset, "mvhd v1")
        timescale = int.from_bytes(body[20:24], "big")
        duration = int.from_bytes(body[24:32], "big")
    elif version == 0:
        timescale = int.from_bytes(body[12:16], "big")
        duration = int.from_bytes(body[16:20], "big")
    else:
        raise ProbeError(f"unsupported mvhd version {version}", offset)
    if timescale <= 0:
        raise ProbeError("mvhd timescale must be positive", offset)
    return timescale, _bmff_duration(duration, version)


def _parse_mdhd(body: bytes, offset: int) -> tuple[int, int, int | None]:
    _need(body, 20, offset, "mdhd v0")
    version = body[0]
    if version == 1:
        _need(body, 32, offset, "mdhd v1")
        timescale = int.from_bytes(body[20:24], "big")
        duration = int.from_bytes(body[24:32], "big")
    elif version == 0:
        timescale = int.from_bytes(body[12:16], "big")
        duration = int.from_bytes(body[16:20], "big")
    else:
        raise ProbeError(f"unsupported mdhd version {version}", offset)
    if timescale <= 0:
        raise ProbeError("mdhd timescale must be positive", offset)
    return version, timescale, _bmff_duration(duration, version)


def _parse_tkhd(body: bytes, offset: int) -> tuple[int, int]:
    _need(body, 24, offset, "tkhd v0")
    version = body[0]
    if version == 1:
        _need(body, 36, offset, "tkhd v1")
        track_id = int.from_bytes(body[20:24], "big")
        duration = int.from_bytes(body[28:36], "big")
    elif version == 0:
        track_id = int.from_bytes(body[12:16], "big")
        duration = int.from_bytes(body[20:24], "big")
    else:
        raise ProbeError(f"unsupported tkhd version {version}", offset)
    return track_id, duration


def _bmff_duration(value: int, version: int) -> int | None:
    """All-ones duration means "unknown" in ISO BMFF; return None for it."""
    if value == (1 << (64 if version == 1 else 32)) - 1:
        return None
    return value


def _parse_hdlr(body: bytes, offset: int) -> bytes:
    _need(body, 12, offset, "hdlr")
    return body[8:12]


def _parse_elst(body: bytes, offset: int) -> tuple[int | None, int | None]:
    _need(body, 8, offset, "elst")
    version = body[0]
    count = int.from_bytes(body[4:8], "big")
    if count == 0:
        return None, None
    if version == 1:
        _need(body, 20, offset, "elst v1 entry")
        segment = int.from_bytes(body[8:16], "big")
        media_time = int.from_bytes(body[16:24], "big", signed=True)
    elif version == 0:
        _need(body, 16, offset, "elst v0 entry")
        segment = int.from_bytes(body[8:12], "big")
        media_time = int.from_bytes(body[12:16], "big", signed=True)
    else:
        raise ProbeError(f"unsupported elst version {version}", offset)
    return segment, media_time


def _parse_sample_entry(box: _Box, data: bytes) -> tuple[str, dict[str, Any]]:
    name = box.box_type.decode("ascii", "replace")
    codec = BMFF_CODECS.get(box.box_type, name)
    body = data[box.data_start:box.data_end]
    params: dict[str, Any] = {}
    if box.box_type in (b"avc1", b"avc3"):
        _need(body, 28, box.offset, name)
        params["width"] = int.from_bytes(body[24:26], "big")
        params["height"] = int.from_bytes(body[26:28], "big")
    elif box.box_type == b"mp4a":
        _need(body, 28, box.offset, name)
        params["channels"] = int.from_bytes(body[16:18], "big")
        rate = int.from_bytes(body[24:28], "big")
        params["sample_rate"] = rate >> 16
    return codec, params


def _parse_bmff(data: bytes, path: str) -> Container:
    result = Container(path=path, container=UNKNOWN)
    top = list(_iter_boxes(data, 0, len(data)))
    if not top or top[0].box_type not in (b"ftyp", b"styp"):
        raise ProbeError("missing ftyp box", 0)
    ftyp_body = data[top[0].data_start:top[0].data_end]
    _need(ftyp_body, 8, top[0].offset, "ftyp")
    major = ftyp_body[0:4].decode("ascii", "replace")
    result.container = f"bmff:{major}" if major.isprintable() else "bmff"
    moov = next((b for b in top if b.box_type == b"moov"), None)
    if moov is None:
        raise ProbeError("missing moov box", top[-1].data_end)

    movie_timescale = None
    movie_duration = None
    for box in _child_boxes(data, moov):
        if box.box_type == b"mvhd":
            movie_timescale, movie_duration = _parse_mvhd(
                data[box.data_start:box.data_end], box.offset
            )
    if movie_timescale is None:
        raise ProbeError("missing mvhd box", moov.offset)
    result.timescale = movie_timescale
    result.duration_ms = _one_shot_ms(movie_duration, movie_timescale)
    result.start_ms = 0

    for trak in (b for b in _child_boxes(data, moov) if b.box_type == b"trak"):
        result.tracks.append(_parse_trak(data, trak, movie_timescale))
    _finalise_tracks(result)
    return result


def _parse_trak(data: bytes, trak: _Box, movie_timescale: int) -> Track:
    track = Track(track_id=None, kind=UNKNOWN, codec=None, timescale=None,
                  start_ms=None, duration_ms=None)
    media_timescale = None
    media_duration = None
    edit_start = None
    edit_segment = None
    for box in _child_boxes(data, trak):
        body = data[box.data_start:box.data_end]
        if box.box_type == b"tkhd":
            track.track_id, _ = _parse_tkhd(body, box.offset)
        elif box.box_type == b"edts":
            for child in _child_boxes(data, box):
                if child.box_type == b"elst":
                    edit_segment, media_time = _parse_elst(
                        data[child.data_start:child.data_end], child.offset
                    )
                    if media_time is not None:
                        edit_start = media_time
        elif box.box_type == b"mdia":
            for child in _child_boxes(data, box):
                child_body = data[child.data_start:child.data_end]
                if child.box_type == b"mdhd":
                    _, media_timescale, media_duration = _parse_mdhd(
                        child_body, child.offset
                    )
                elif child.box_type == b"hdlr":
                    handler = _parse_hdlr(child_body, child.offset)
                    track.kind = BMFF_KINDS.get(handler, UNKNOWN)
                elif child.box_type == b"minf":
                    for minf_child in _child_boxes(data, child):
                        if minf_child.box_type == b"stbl":
                            _parse_stbl(data, minf_child, track)
    track.timescale = media_timescale
    if edit_start is not None and edit_start > 0:
        track.start_ms = _one_shot_ms(edit_start, media_timescale)
    else:
        track.start_ms = 0
    if edit_segment is not None:
        track.duration_ms = _one_shot_ms(edit_segment, movie_timescale)
    else:
        track.duration_ms = _one_shot_ms(media_duration, media_timescale)
    return track


def _parse_stbl(data: bytes, stbl: _Box, track: Track) -> None:
    for box in _child_boxes(data, stbl):
        if box.box_type != b"stsd":
            continue
        body = data[box.data_start:box.data_end]
        _need(body, 8, box.offset, "stsd")
        count = int.from_bytes(body[4:8], "big")
        if count == 0:
            track.problems.append("stsd declares no sample entries")
            return
        entry = _read_box(data, box.data_start + 8, box.data_end)
        track.codec, track.params = _parse_sample_entry(entry, data)
        return


def _sniff(data: bytes) -> str:
    if data[:4] == b"\x1a\x45\xdf\xa3":
        return "ebml"
    if len(data) >= 8 and data[4:8] in BMFF_BOX_TYPES:
        return "bmff"
    return UNKNOWN


def probe_bytes(data: bytes, path: str = "<memory>") -> Container:
    """Probe raw bytes, returning a Container even for malformed input."""

    result = Container(path=path, size=len(data), sha256=hashlib.sha256(data).hexdigest())
    try:
        if not data:
            raise ProbeError("empty file", 0)
        kind = _sniff(data)
        if kind == "ebml":
            parsed = _parse_ebml(data, path)
        elif kind == "bmff":
            parsed = _parse_bmff(data, path)
        else:
            raise ProbeError("unrecognised container signature", 0)
    except ProbeError as exc:
        result.error = exc.message
        result.error_offset = exc.offset
        return result
    parsed.size = result.size
    parsed.sha256 = result.sha256
    return parsed


def _hash_file(path: str) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def probe_path(path: str, max_bytes: int = MAX_FILE_BYTES) -> Container:
    """Probe a file on disk. Filesystem and size problems become file errors."""

    result = Container(path=path)
    try:
        stat = os.stat(path, follow_symlinks=False)
        if not os.path.isfile(path) or os.path.islink(path):
            raise ProbeError("not a regular file (symlinks are not followed)", 0)
        result.size = stat.st_size
        if stat.st_size > max_bytes:
            raise ProbeError(
                f"file is {stat.st_size} bytes, limit is {max_bytes} bytes", 0
            )
        result.sha256 = _hash_file(path)
        with open(path, "rb") as handle:
            data = handle.read()
    except OSError as exc:
        result.error = f"cannot read file: {exc.strerror or exc}"
        result.error_offset = 0
        return result
    except ProbeError as exc:
        result.error = exc.message
        result.error_offset = exc.offset
        return result
    probed = probe_bytes(data, path)
    probed.size = result.size
    return probed
