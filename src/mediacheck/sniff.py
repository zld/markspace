"""Self-contained container sniffing for ISO BMFF (MP4) and RIFF/WAVE.

Only header-level metadata is read. Every read goes through :class:`Reader`,
which enforces file bounds so malformed sizes can never trigger an
out-of-bounds read. Structural problems are raised as :class:`SniffError`
with a byte offset and a box path; harmless oddities are recorded as
warnings on the sniff context.
"""

from __future__ import annotations

import struct
from dataclasses import dataclass, field
from typing import BinaryIO, Iterator

MAX_FILE_BYTES = 2 * 1024 * 1024 * 1024  # 2 GiB per file
MAX_BOXES = 4096
MAX_DEPTH = 12
MAX_BRANDS = 32
MAX_SAMPLE_ENTRIES = 64
MAX_EDIT_ENTRIES = 1024
MAX_WAV_CHUNKS = 4096

FTYP = b"ftyp"
MOOV = b"moov"
MDAT = b"mdat"
UUID = b"uuid"

# Boxes whose payload we descend into, mapped to their child layout.
CONTAINER_TYPES = {
    b"moov", b"trak", b"mdia", b"minf", b"stbl", b"edts", b"dinf",
}
# Boxes we understand well enough to ignore without warning.
IGNORABLE_TYPES = {
    b"free", b"skip", b"wide", b"mdat", b"styp", b"moof", b"mfra", b"pdin",
    b"udta", b"meta", b"ilst", b"iods", b"mvex", b"trex", b"mfhd", b"traf",
    b"tfhd", b"tfdt", b"trun", b"sidx", b"ssix", b"prft", b"emsg", b"btrt",
    b"pasp", b"clap", b"colr", b"gama", b"sv3d", b"proj", b"dOps", b"alac",
    b"wave", b"chan", b"srcp", b"dac3", b"dec3", b"dvcC", b"dvvC", b"hvcC",
    b"avcC", b"vpcC", b"av1C", b"esds", b"frma", b"schm", b"schi", b"sinf",
    b"stsd", b"stts", b"stsc", b"stsz", b"stz2", b"stco", b"co64", b"stss",
    b"ctts", b"sdtp", b"sbgp", b"sgpd", b"subs", b"saiz", b"saio", b"padb",
    b"stdp", b"dref", b"url ", b"urn ", b"vmhd", b"smhd", b"hmhd", b"nmhd",
    b"elst", b"tkhd", b"mdhd", b"hdlr", b"mvhd", b"ftyp",
}
# Sample entry formats we accept as a codec identifier.
VIDEO_CODECS = {b"avc1", b"avc3", b"hvc1", b"hev1", b"vp09", b"av01", b"mp4v"}
AUDIO_CODECS = {b"mp4a", b"opus", b"alac", b"twos", b"sowt", b"lpcm", b"fl32", b"fl64", b"ulaw", b"alaw", b"ac-3", b"ec-3"}
SUBTITLE_CODECS = {b"tx3g", b"wvtt", b"stpp", b"c608", b"c708"}


class SniffError(Exception):
    """Fatal structural error with a locatable position."""

    def __init__(self, code: str, message: str, *, offset: int | None = None,
                 path: str | None = None):
        super().__init__(message)
        self.code = code
        self.message = message
        self.offset = offset
        self.path = path

    def to_dict(self) -> dict:
        return {
            "code": self.code,
            "message": self.message,
            "offset": self.offset,
            "path": self.path,
        }


@dataclass
class Warning:
    code: str
    message: str
    offset: int | None = None
    path: str | None = None

    def to_dict(self) -> dict:
        return {
            "code": self.code,
            "message": self.message,
            "offset": self.offset,
            "path": self.path,
        }


@dataclass
class BoxHeader:
    fourcc: bytes
    size: int
    header_size: int
    start: int
    end: int
    path: str
    uuid: bytes | None = None

    @property
    def name(self) -> str:
        return fourcc_str(self.fourcc)

    @property
    def payload_start(self) -> int:
        return self.start + self.header_size

    @property
    def payload_size(self) -> int:
        return self.end - self.payload_start


def fourcc_str(value: bytes) -> str:
    return value.decode("latin-1", "replace")


def iter_boxes(reader: "Reader", start: int, end: int, ctx: "SniffContext",
               path: str, depth: int) -> Iterator[BoxHeader]:
    """Yield validated box headers inside [start, end).

    Raises SniffError on a short header, impossible size, or overflow past
    the parent boundary. Never advances outside the parent range.
    """
    if depth > MAX_DEPTH:
        raise SniffError("depth_limit", f"box nesting exceeds {MAX_DEPTH}",
                         offset=start, path=path)
    offset = start
    while offset < end:
        if end - offset < 8:
            raise SniffError("truncated_box", "fewer than 8 bytes left for a box header",
                             offset=offset, path=path)
        header = reader.read_box_header(offset, end, path)
        ctx.boxes += 1
        if ctx.boxes > MAX_BOXES:
            raise SniffError("box_limit", f"more than {MAX_BOXES} boxes read",
                             offset=header.start, path=path)
        yield header
        if header.end <= offset:  # defensive: guarantee forward progress
            raise SniffError("invalid_size", "box does not advance the cursor",
                             offset=offset, path=path)
        offset = header.end


class Reader:
    """Bounded reader: no seek/read may escape [0, file_size]."""

    def __init__(self, handle: BinaryIO, size: int):
        self.handle = handle
        self.size = size

    def read_at(self, offset: int, count: int, *, path: str | None = None) -> bytes:
        if offset < 0 or count < 0:
            raise SniffError("invalid_offset", f"negative read offset={offset} count={count}",
                             offset=offset, path=path)
        if offset + count > self.size:
            raise SniffError(
                "out_of_bounds",
                f"read of {count} bytes at {offset} exceeds file size {self.size}",
                offset=offset, path=path,
            )
        self.handle.seek(offset)
        data = self.handle.read(count)
        if len(data) != count:
            raise SniffError(
                "truncated_read",
                f"wanted {count} bytes at {offset}, got {len(data)}",
                offset=offset, path=path,
            )
        return data

    def read_box_header(self, offset: int, parent_end: int, path: str) -> BoxHeader:
        base = self.read_at(offset, 8, path=path)
        size32, fourcc = struct.unpack(">I4s", base)
        header_size = 8
        if size32 == 1:
            if parent_end - offset < 16:
                raise SniffError("truncated_box", "largesize box header is truncated",
                                 offset=offset, path=path)
            (size,) = struct.unpack(">Q", self.read_at(offset + 8, 8, path=path))
            header_size = 16
        elif size32 == 0:
            size = parent_end - offset
        else:
            size = size32
        uuid = None
        if fourcc == UUID:
            if parent_end - offset < header_size + 16:
                raise SniffError("truncated_box", "uuid box header is truncated",
                                 offset=offset, path=path)
            uuid = self.read_at(offset + header_size, 16, path=path)
            header_size += 16
        if size < header_size:
            raise SniffError(
                "invalid_size",
                f"box {fourcc_str(fourcc)!r} size {size} is smaller than header {header_size}",
                offset=offset, path=path,
            )
        end = offset + size
        if end > parent_end:
            raise SniffError(
                "out_of_bounds",
                f"box {fourcc_str(fourcc)!r} ends at {end}, past boundary {parent_end}",
                offset=offset, path=path,
            )
        box_path = f"{path}/{fourcc_str(fourcc)}" if path else fourcc_str(fourcc)
        return BoxHeader(fourcc, size, header_size, offset, end, box_path, uuid)


@dataclass
class SniffContext:
    boxes: int = 0
    warnings: list[Warning] = field(default_factory=list)

    def warn(self, code: str, message: str, *, offset: int | None = None,
             path: str | None = None) -> None:
        self.warnings.append(Warning(code, message, offset, path))


# ---------------------------------------------------------------------------
# Normalized sniff result
# ---------------------------------------------------------------------------

@dataclass
class SniffTrack:
    track_id: int | None
    handler: str | None            # vide / soun / text / sbtl ...
    timescale: int | None
    duration_units: int | None
    start_time_units: int | None   # edit-list media_time (head trim), track units
    codec: str | None
    codec_raw: str | None
    language: str | None
    width: int | None
    height: int | None
    sample_rate: int | None
    channels: int | None
    bit_depth: int | None
    start_movie_units: int | None = None  # empty-edit offset, in movie units
    reasons: dict[str, str] = field(default_factory=dict)
    warnings: list[Warning] = field(default_factory=list)


@dataclass
class SniffResult:
    container: str
    file_size: int
    brands: list[str]
    movie_timescale: int | None
    movie_duration_units: int | None
    tracks: list[SniffTrack]
    warnings: list[Warning]
    wav_format_tag: int | None = None

    def to_dict(self) -> dict:
        return {
            "container": self.container,
            "file_size": self.file_size,
            "brands": list(self.brands),
            "movie_timescale": self.movie_timescale,
            "movie_duration_units": self.movie_duration_units,
            "wav_format_tag": self.wav_format_tag,
            "tracks": [
                {
                    "track_id": t.track_id,
                    "handler": t.handler,
                    "timescale": t.timescale,
                    "duration_units": t.duration_units,
                    "start_time_units": t.start_time_units,
                    "start_movie_units": t.start_movie_units,
                    "codec": t.codec,
                    "codec_raw": t.codec_raw,
                    "language": t.language,
                    "width": t.width,
                    "height": t.height,
                    "sample_rate": t.sample_rate,
                    "channels": t.channels,
                    "bit_depth": t.bit_depth,
                    "reasons": dict(t.reasons),
                    "warnings": [w.to_dict() for w in t.warnings],
                }
                for t in self.tracks
            ],
            "warnings": [w.to_dict() for w in self.warnings],
        }


# ---------------------------------------------------------------------------
# Codec tables
# ---------------------------------------------------------------------------

CODEC_NAMES = {
    b"avc1": "h264", b"avc3": "h264", b"hvc1": "hevc", b"hev1": "hevc",
    b"vp09": "vp9", b"av01": "av1", b"mp4v": "mpeg4",
    b"mp4a": "aac", b"opus": "opus", b"alac": "alac", b"twos": "pcm_s16be",
    b"sowt": "pcm_s16le", b"lpcm": "pcm", b"fl32": "pcm_f32",
    b"fl64": "pcm_f64", b"ulaw": "pcm_mulaw", b"alaw": "pcm_alaw",
    b"ac-3": "ac3", b"ec-3": "eac3",
    b"tx3g": "tx3g", b"wvtt": "webvtt", b"stpp": "ttml",
}
# ESDS objectTypeIndication -> codec name for mp4a entries.
ESDS_OBJECT_TYPES = {
    0x40: "aac", 0x66: "aac", 0x67: "aac", 0x68: "aac",
    0x69: "mp3", 0x6B: "mp3", 0xA5: "ac3", 0xA6: "eac3",
}
HANDLER_KINDS = {b"vide": "video", b"soun": "audio", b"text": "text",
                 b"sbtl": "subtitle", b"subt": "subtitle", b"meta": "metadata"}


def _be(data: bytes, offset: int, fmt: str) -> tuple:
    size = struct.calcsize(fmt)
    if offset + size > len(data):
        raise SniffError("truncated_field",
                         f"field {fmt} at payload {offset} exceeds {len(data)} bytes")
    return struct.unpack_from(fmt, data, offset)


def _full_box(data: bytes, path: str) -> tuple[int, int, bytes]:
    if len(data) < 4:
        raise SniffError("truncated_box", "full box header is truncated", path=path)
    version = data[0]
    flags = int.from_bytes(data[1:4], "big")
    return version, flags, data[4:]


def _decode_language(code: int) -> str | None:
    if code == 0:
        return None
    chars = []
    for shift in (10, 5, 0):
        idx = (code >> shift) & 0x1F
        # 5-bit ISO 639-2/T values are 1..26; 0 is padding.
        if idx == 0 or idx > 26:
            return None
        chars.append(chr(0x60 + idx))
    return "".join(chars)


def _mp4_payload(reader: Reader, box: BoxHeader) -> bytes:
    return reader.read_at(box.payload_start, box.payload_size, path=box.path)


def _require_payload(box: BoxHeader, minimum: int, needed: str) -> None:
    if box.payload_size < minimum:
        raise SniffError("truncated_box",
                         f"{box.name} needs {minimum} bytes for {needed}, has {box.payload_size}",
                         offset=box.start, path=box.path)


# ---------------------------------------------------------------------------
# MP4 / ISO BMFF
# ---------------------------------------------------------------------------

def sniff_mp4(reader: Reader, ctx: SniffContext) -> SniffResult:
    top = reader.read_box_header(0, reader.size, "")
    if top.fourcc != FTYP:
        raise SniffError("missing_ftyp", "first box is not ftyp",
                         offset=0, path="")
    _require_payload(top, 8, "major brand and minor version")
    ftyp = _mp4_payload(reader, top)
    brands = [fourcc_str(ftyp[0:4])]
    for i in range(8, len(ftyp) - 3, 4):
        if len(brands) >= MAX_BRANDS:
            ctx.warn("brand_limit", f"more than {MAX_BRANDS} compatible brands",
                     offset=top.start, path=top.path)
            break
        brands.append(fourcc_str(ftyp[i:i + 4]))

    movie_timescale: int | None = None
    movie_duration: int | None = None
    tracks: list[SniffTrack] = []
    moov_seen = False
    moov_count = 0

    for box in iter_boxes(reader, 0, reader.size, ctx, "", 0):
        if box.fourcc == FTYP:
            continue
        if box.fourcc == MOOV:
            moov_count += 1
            if moov_count > 1:
                raise SniffError("duplicate_moov", "more than one moov box",
                                 offset=box.start, path=box.path)
            moov_seen = True
            movie_timescale, movie_duration, moov_tracks = _parse_moov(
                reader, box, ctx)
            tracks.extend(moov_tracks)
            continue
        if box.fourcc in IGNORABLE_TYPES or box.fourcc in (b"mdat", b"moof",
                                                           b"mfra", b"styp"):
            continue
        if box.fourcc == b"uuid":
            continue
        # Unknown top-level box: record but do not fail; it is structurally
        # sound because iter_boxes already validated its bounds.
        ctx.warn("unknown_box", f"unhandled top-level box {box.name!r} "
                 f"({box.size} bytes) was skipped", offset=box.start, path=box.path)

    if not moov_seen:
        raise SniffError("missing_moov", "no moov box found; metadata unavailable",
                         offset=0, path="")

    return SniffResult(
        container="mp4", file_size=reader.size, brands=brands,
        movie_timescale=movie_timescale, movie_duration_units=movie_duration,
        tracks=tracks, warnings=ctx.warnings,
    )


def _parse_moov(reader: Reader, box: BoxHeader, ctx: SniffContext
                ) -> tuple[int | None, int | None, list[SniffTrack]]:
    timescale = None
    duration = None
    tracks: list[SniffTrack] = []
    for child in iter_boxes(reader, box.payload_start, box.end, ctx,
                            box.path, 1):
        if child.fourcc == b"mvhd":
            timescale, duration = _parse_mvhd(reader, child, ctx)
        elif child.fourcc == b"trak":
            tracks.append(_parse_trak(reader, child, ctx))
        elif child.fourcc in CONTAINER_TYPES:
            raise SniffError("unexpected_container",
                             f"unexpected container {child.name} inside moov",
                             offset=child.start, path=child.path)
        elif child.fourcc not in IGNORABLE_TYPES:
            ctx.warn("unknown_box", f"unhandled box {child.name!r} inside moov",
                     offset=child.start, path=child.path)
    return timescale, duration, tracks


def _parse_mvhd(reader: Reader, box: BoxHeader, ctx: SniffContext
                ) -> tuple[int | None, int | None]:
    _require_payload(box, 20, "mvhd")
    data = _mp4_payload(reader, box)
    version, _flags, body = _full_box(data, box.path)
    if version == 1:
        if len(body) < 28:
            raise SniffError("truncated_box", "mvhd v1 is truncated",
                             offset=box.start, path=box.path)
        ts = int.from_bytes(body[16:20], "big")
        dur = int.from_bytes(body[20:28], "big")
    else:
        ts = int.from_bytes(body[8:12], "big")
        dur = int.from_bytes(body[12:16], "big")
    if ts == 0:
        ctx.warn("zero_timescale", "mvhd timescale is zero; durations cannot be converted",
                 offset=box.start, path=box.path)
        ts = None
    if dur == 0xFFFFFFFF or dur == 0xFFFFFFFFFFFFFFFF:
        ctx.warn("unknown_duration", "mvhd duration is the unknown sentinel",
                 offset=box.start, path=box.path)
        dur = None
    return ts, dur


def _parse_trak(reader: Reader, box: BoxHeader, ctx: SniffContext) -> SniffTrack:
    track = SniffTrack(None, None, None, None, None, None, None, None,
                       None, None, None, None, None)
    timescale = None
    duration_units = None
    media_time = None
    presentation_start = None
    stsd_info: dict = {}
    hdlr_type = None
    tkhd_width = tkhd_height = None
    hdlr_name = None
    seen_mdia = False
    for child in iter_boxes(reader, box.payload_start, box.end, ctx,
                            box.path, 2):
        if child.fourcc == b"tkhd":
            track_id, tkhd_width, tkhd_height = _parse_tkhd(reader, child, ctx)
            track.track_id = track_id
        elif child.fourcc == b"edts":
            presentation_start, media_time = _parse_edts(reader, child, ctx)
        elif child.fourcc == b"mdia":
            if seen_mdia:
                raise SniffError("duplicate_mdia",
                                 "more than one mdia box inside trak",
                                 offset=child.start, path=child.path)
            seen_mdia = True
            timescale, duration_units, hdlr_type, hdlr_name, stsd_info = \
                _parse_mdia(reader, child, ctx)
        elif child.fourcc in CONTAINER_TYPES and child.fourcc != b"edts":
            raise SniffError("unexpected_container",
                             f"unexpected container {child.name} inside trak",
                             offset=child.start, path=child.path)
        elif child.fourcc not in IGNORABLE_TYPES:
            ctx.warn("unknown_box", f"unhandled box {child.name!r} inside trak",
                     offset=child.start, path=child.path)
    track.timescale = timescale
    track.duration_units = duration_units
    track.start_time_units = media_time
    track.start_movie_units = presentation_start
    track.handler = hdlr_type
    track.language = stsd_info.get("language") or hdlr_name
    if track.handler == "video":
        track.width = tkhd_width if tkhd_width is not None else stsd_info.get("width")
        track.height = tkhd_height if tkhd_height is not None else stsd_info.get("height")
    elif track.handler == "audio":
        track.sample_rate = stsd_info.get("sample_rate")
        track.channels = stsd_info.get("channels")
        track.bit_depth = stsd_info.get("bit_depth")
    if stsd_info:
        track.codec = stsd_info.get("codec")
        track.codec_raw = stsd_info.get("codec_raw")
        if stsd_info.get("reasons"):
            track.reasons.update(stsd_info["reasons"])
    _mark_missing(track, seen_mdia)
    return track


def _mark_missing(track: SniffTrack, seen_mdia: bool = True) -> None:
    if track.track_id is None:
        track.reasons["track_id"] = "tkhd box missing or unreadable"
    if not seen_mdia:
        # A trak with no mdia is structurally odd but not a read error; record
        # it and keep the batch going instead of rejecting the whole file.
        track.reasons.setdefault("handler", "trak has no mdia box")
        track.reasons.setdefault("timescale", "trak has no mdia box")
        track.reasons.setdefault("duration_seconds", "trak has no mdia box")
        return
    if track.handler is None:
        track.reasons["handler"] = "mdia/hdlr box missing or unreadable"
    if track.timescale is None:
        track.reasons["timescale"] = "mdia/mdhd timescale missing or zero"
    if track.duration_units is None:
        track.reasons["duration"] = "mdia/mdhd duration missing or unknown sentinel"
    if track.codec is None and "codec" not in track.reasons:
        track.reasons["codec"] = "stsd sample entry missing or unsupported"
    if track.handler == "video":
        if track.width is None:
            track.reasons.setdefault("width", "tkhd/stsd width unavailable")
        if track.height is None:
            track.reasons.setdefault("height", "tkhd/stsd height unavailable")
    if track.handler == "audio":
        if track.sample_rate is None:
            track.reasons.setdefault("sample_rate", "stsd audio entry sample rate unavailable")
        if track.channels is None:
            track.reasons.setdefault("channels", "stsd audio entry channel count unavailable")


def _parse_tkhd(reader: Reader, box: BoxHeader, ctx: SniffContext
                ) -> tuple[int | None, int | None, int | None]:
    _require_payload(box, 16, "tkhd")
    data = _mp4_payload(reader, box)
    version, _flags, body = _full_box(data, box.path)
    if version == 1:
        # creation, modification, track_id, reserved, duration (8-byte times)
        if len(body) < 32:
            raise SniffError("truncated_box", "tkhd v1 is truncated",
                             offset=box.start, path=box.path)
        track_id = int.from_bytes(body[16:20], "big")
        pos = 32
    else:
        if len(body) < 20:
            raise SniffError("truncated_box", "tkhd v0 is truncated",
                             offset=box.start, path=box.path)
        track_id = int.from_bytes(body[8:12], "big")
        pos = 20
    width_fixed = height_fixed = None
    # skip reserved(8) + layer(2) + alternate_group(2) + volume(2) + reserved(2)
    pos += 16
    # matrix is 36 bytes, then width and height are 16.16 fixed point
    if len(body) >= pos + 36 + 8:
        width_fixed = int.from_bytes(body[pos + 36:pos + 40], "big")
        height_fixed = int.from_bytes(body[pos + 40:pos + 44], "big")
    width = round(width_fixed / 65536) if width_fixed else None
    height = round(height_fixed / 65536) if height_fixed else None
    if track_id == 0:
        ctx.warn("zero_track_id", "tkhd track_id is zero",
                 offset=box.start, path=box.path)
        track_id = None
    return track_id, width, height


def _parse_edts(reader: Reader, box: BoxHeader, ctx: SniffContext
                ) -> tuple[int | None, int | None]:
    """Return (presentation_start_in_movie, media_time) from edts/elst.

    An empty edit (media_time == -1) delays the track by its segment
    duration, measured in movie timescale. A non-empty edit with a positive
    media_time trims the head of the media. Both are converted to movie
    units so callers can build a unified time base.
    """
    media_time = None
    presentation_start = None
    for child in iter_boxes(reader, box.payload_start, box.end, ctx,
                            box.path, 3):
        if child.fourcc != b"elst":
            if child.fourcc not in IGNORABLE_TYPES:
                ctx.warn("unknown_box", f"unhandled box {child.name!r} inside edts",
                         offset=child.start, path=child.path)
            continue
        data = _mp4_payload(reader, child)
        version, _flags, body = _full_box(data, child.path)
        if len(body) < 4:
            raise SniffError("truncated_box", "elst entry count is truncated",
                             offset=child.start, path=child.path)
        count = int.from_bytes(body[0:4], "big")
        if count > MAX_EDIT_ENTRIES:
            raise SniffError("edit_limit", f"elst declares {count} entries "
                             f"(limit {MAX_EDIT_ENTRIES})",
                             offset=child.start, path=child.path)
        entry = 20 if version == 1 else 12
        if len(body) < 4 + count * entry:
            raise SniffError("truncated_box",
                             f"elst needs {count} entries of {entry} bytes",
                             offset=child.start, path=child.path)
        offset = 0
        for i in range(count):
            base = 4 + i * entry
            if version == 1:
                seg_dur = int.from_bytes(body[base:base + 8], "big", signed=True)
                mt = int.from_bytes(body[base + 8:base + 16], "big", signed=True)
            else:
                seg_dur = int.from_bytes(body[base:base + 4], "big", signed=True)
                mt = int.from_bytes(body[base + 4:base + 8], "big", signed=True)
            if mt == -1:
                # Empty edit: the track is silent/blank for seg_dur movie units.
                offset += seg_dur
                continue
            media_time = mt if mt > 0 else 0
            presentation_start = offset
            break
        else:
            media_time = None
            presentation_start = None
        if count and presentation_start is None and media_time is None:
            ctx.warn("edit_list_empty",
                     "elst contains only empty edits; track never presents",
                     offset=child.start, path=child.path)
    return presentation_start, media_time
