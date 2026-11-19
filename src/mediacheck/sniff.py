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


def _parse_mdia(reader: Reader, box: BoxHeader, ctx: SniffContext):
    timescale = None
    duration = None
    hdlr_type = None
    hdlr_name = None
    language = None
    stsd_info: dict = {}
    for child in iter_boxes(reader, box.payload_start, box.end, ctx,
                            box.path, 3):
        if child.fourcc == b"mdhd":
            timescale, duration, language = _parse_mdhd(reader, child, ctx)
        elif child.fourcc == b"hdlr":
            hdlr_type, hdlr_name = _parse_hdlr(reader, child, ctx)
        elif child.fourcc == b"minf":
            stsd_info = _parse_minf(reader, child, ctx)
        elif child.fourcc in CONTAINER_TYPES:
            raise SniffError("unexpected_container",
                             f"unexpected container {child.name} inside mdia",
                             offset=child.start, path=child.path)
        elif child.fourcc not in IGNORABLE_TYPES:
            ctx.warn("unknown_box", f"unhandled box {child.name!r} inside mdia",
                     offset=child.start, path=child.path)
    stsd_info["language"] = language or hdlr_name
    return timescale, duration, hdlr_type, hdlr_name, stsd_info


def _parse_mdhd(reader: Reader, box: BoxHeader, ctx: SniffContext
                ) -> tuple[int | None, int | None, str | None]:
    _require_payload(box, 20, "mdhd")
    data = _mp4_payload(reader, box)
    version, _flags, body = _full_box(data, box.path)
    if version == 1:
        if len(body) < 28:
            raise SniffError("truncated_box", "mdhd v1 is truncated",
                             offset=box.start, path=box.path)
        ts = int.from_bytes(body[16:20], "big")
        dur = int.from_bytes(body[20:28], "big")
        lang_code = int.from_bytes(body[28:30], "big") if len(body) >= 30 else 0
    else:
        if len(body) < 16:
            raise SniffError("truncated_box", "mdhd v0 is truncated",
                             offset=box.start, path=box.path)
        ts = int.from_bytes(body[8:12], "big")
        dur = int.from_bytes(body[12:16], "big")
        lang_code = int.from_bytes(body[16:18], "big") if len(body) >= 18 else 0
    if ts == 0:
        ctx.warn("zero_timescale", "mdhd timescale is zero",
                 offset=box.start, path=box.path)
        ts = None
    if dur in (0xFFFFFFFF, 0xFFFFFFFFFFFFFFFF):
        ctx.warn("unknown_duration", "mdhd duration is the unknown sentinel",
                 offset=box.start, path=box.path)
        dur = None
    return ts, dur, _decode_language(lang_code)


def _parse_hdlr(reader: Reader, box: BoxHeader, ctx: SniffContext
                ) -> tuple[str | None, str | None]:
    _require_payload(box, 12, "hdlr")
    data = _mp4_payload(reader, box)
    handler_bytes = data[8:12]
    kind = HANDLER_KINDS.get(handler_bytes)
    if kind is None:
        ctx.warn("unknown_handler",
                 f"unsupported handler type {fourcc_str(handler_bytes)!r}",
                 offset=box.start, path=box.path)
        kind = fourcc_str(handler_bytes)
    name = None
    if len(data) >= 24:
        name_bytes = data[24:].split(b"\x00", 1)[0]
        if name_bytes:
            name = name_bytes.decode("utf-8", "replace")
    return kind, name


def _parse_minf(reader: Reader, box: BoxHeader, ctx: SniffContext) -> dict:
    info: dict = {}
    for child in iter_boxes(reader, box.payload_start, box.end, ctx,
                            box.path, 4):
        if child.fourcc == b"stbl":
            info = _parse_stbl(reader, child, ctx)
        elif child.fourcc in CONTAINER_TYPES:
            continue  # dinf
        elif child.fourcc not in IGNORABLE_TYPES:
            ctx.warn("unknown_box", f"unhandled box {child.name!r} inside minf",
                     offset=child.start, path=child.path)
    return info


def _parse_stbl(reader: Reader, box: BoxHeader, ctx: SniffContext) -> dict:
    info: dict = {}
    for child in iter_boxes(reader, box.payload_start, box.end, ctx,
                            box.path, 5):
        if child.fourcc == b"stsd":
            info.update(_parse_stsd(reader, child, ctx))
        elif child.fourcc not in IGNORABLE_TYPES:
            ctx.warn("unknown_box", f"unhandled box {child.name!r} inside stbl",
                     offset=child.start, path=child.path)
    return info


def _parse_stsd(reader: Reader, box: BoxHeader, ctx: SniffContext) -> dict:
    _require_payload(box, 8, "stsd")
    data = _mp4_payload(reader, box)
    _version, _flags, body = _full_box(data, box.path)
    entry_count = int.from_bytes(body[0:4], "big") if len(body) >= 4 else 0
    if entry_count > MAX_SAMPLE_ENTRIES:
        raise SniffError("sample_entry_limit",
                         f"stsd declares {entry_count} entries (limit {MAX_SAMPLE_ENTRIES})",
                         offset=box.start, path=box.path)
    result: dict = {}
    pos = 4
    for index in range(entry_count):
        if pos + 8 > len(body):
            ctx.warn("truncated_box", "stsd entry table ends early",
                     offset=box.start, path=box.path)
            break
        size = int.from_bytes(body[pos:pos + 4], "big")
        fourcc = body[pos + 4:pos + 8]
        if size < 8 or pos + size > len(body):
            ctx.warn("invalid_size",
                     f"stsd entry {fourcc_str(fourcc)!r} has size {size} "
                     f"at payload {pos}", offset=box.start, path=box.path)
            break
        payload = body[pos + 8:pos + size]
        display = fourcc_str(fourcc)
        codec = CODEC_NAMES.get(fourcc)
        info = _parse_sample_entry(fourcc, payload, box, display, ctx)
        if index == 0:
            result = info
            result["codec_raw"] = display
            if codec:
                result["codec"] = codec
            else:
                result["codec"] = display
                result.setdefault("reasons", {})["codec"] = (
                    f"sample entry {display!r} is not in the supported codec table")
                ctx.warn("unsupported_codec",
                         f"sample entry {display!r} is not a known codec",
                         offset=box.start, path=box.path)
        elif info.get("codec_raw") != display:
            ctx.warn("mixed_sample_entries",
                     f"stsd entry {index} is {display!r}, differing from first entry",
                     offset=box.start, path=box.path)
        pos += size
    return result


def _parse_sample_entry(fourcc: bytes, payload: bytes, box: BoxHeader,
                        display: str, ctx: SniffContext) -> dict:
    info: dict = {}
    reasons: dict[str, str] = {}
    if fourcc in VIDEO_CODECS:
        # VisualSampleEntry: 6 reserved + 2 data_ref_index, then 16 bytes
        # pre_defined/reserved, width(2) height(2), resolutions, etc.
        if len(payload) < 78:
            ctx.warn("truncated_sample_entry",
                     f"video sample entry {display!r} has {len(payload)} bytes, need 78",
                     offset=box.start, path=box.path)
            return {"reasons": {"width": "video sample entry truncated",
                                "height": "video sample entry truncated"}}
        info["width"] = int.from_bytes(payload[24:26], "big")
        info["height"] = int.from_bytes(payload[26:28], "big")
        info["bit_depth"] = int.from_bytes(payload[74:76], "big")
    elif fourcc in AUDIO_CODECS:
        if len(payload) < 28:
            ctx.warn("truncated_sample_entry",
                     f"audio sample entry {display!r} has {len(payload)} bytes, need 28",
                     offset=box.start, path=box.path)
            return {"reasons": {"sample_rate": "audio sample entry truncated",
                                "channels": "audio sample entry truncated"}}
        channels = int.from_bytes(payload[16:18], "big")
        bit_depth = int.from_bytes(payload[18:20], "big")
        rate_fixed = int.from_bytes(payload[24:28], "big")
        info["channels"] = channels
        info["bit_depth"] = bit_depth
        rate = round(rate_fixed / 65536)
        if rate > 0:
            info["sample_rate"] = rate
        else:
            reasons["sample_rate"] = "sample entry stores rate 0; check esds"
        if fourcc == b"mp4a":
            esds = _find_esds(payload[28:], box, ctx)
            if esds:
                oti, rate2 = esds
                info["codec"] = ESDS_OBJECT_TYPES.get(oti, f"mpeg4-audio-0x{oti:02x}")
                if info.get("sample_rate") is None and rate2:
                    info["sample_rate"] = rate2
    return {**info, "reasons": reasons}


def _find_esds(payload: bytes, box: BoxHeader, ctx: SniffContext
               ) -> tuple[int, int | None] | None:
    """Walk child boxes of an audio sample entry to find esds.

    Returns (objectTypeIndication, average bitrate sample rate hint) or None.
    """
    pos = 0
    while pos + 8 <= len(payload):
        size = int.from_bytes(payload[pos:pos + 4], "big")
        fourcc = payload[pos + 4:pos + 8]
        if size < 8 or pos + size > len(payload):
            break
        if fourcc == b"esds":
            body = payload[pos + 8:pos + size]
            return _parse_esds(body, box)
        pos += size
    return None


def _parse_esds(body: bytes, box: BoxHeader) -> tuple[int, int | None] | None:
    if len(body) < 4:
        return None
    data = body[4:]  # skip full-box header
    pos = 0
    oti = None
    # ES_Descriptor tag 0x03, DecoderConfigDescriptor 0x04, DecSpecificInfo 0x05
    while pos < len(data):
        tag = data[pos]
        pos += 1
        length = 0
        for _ in range(4):
            if pos >= len(data):
                return None
            b = data[pos]
            pos += 1
            length = (length << 7) | (b & 0x7F)
            if not b & 0x80:
                break
        if pos + length > len(data):
            return None
        if tag == 0x04 and length >= 13:
            oti = data[pos]
            max_bitrate = int.from_bytes(data[pos + 5:pos + 9], "big")
            avg_bitrate = int.from_bytes(data[pos + 9:pos + 13], "big")
            rate_hint = avg_bitrate if avg_bitrate else None
            return (oti, rate_hint) if oti is not None else None
        pos += length
    return None


# ---------------------------------------------------------------------------
# RIFF / WAVE
# ---------------------------------------------------------------------------

WAV_FORMAT_NAMES = {
    0x0001: "pcm_s16le", 0x0003: "pcm_f32", 0x0006: "pcm_alaw",
    0x0007: "pcm_mulaw", 0xFFFE: "pcm_extensible",
}
WAV_SUPPORTED_TAGS = {0x0001, 0x0003, 0x0006, 0x0007, 0xFFFE}


def sniff_wav(reader: Reader, ctx: SniffContext) -> SniffResult:
    head = reader.read_at(0, 12, path="")
    if head[0:4] != b"RIFF" or head[8:12] != b"WAVE":
        raise SniffError("bad_riff", "missing RIFF/WAVE signature", offset=0, path="")
    riff_size = int.from_bytes(head[4:8], "little")
    declared_end = 8 + riff_size
    warnings = ctx.warnings
    if declared_end > reader.size:
        warnings.append(Warning(
            "riff_size_overflow",
            f"RIFF declares {riff_size} bytes but file has {reader.size - 8}; "
            "using actual file size",
            offset=4, path="riff"))
        riff_end = reader.size
    elif declared_end < reader.size:
        ctx.warn("trailing_bytes",
                 f"{reader.size - declared_end} bytes follow the declared RIFF chunk",
                 offset=declared_end, path="riff")
        riff_end = declared_end
    else:
        riff_end = declared_end

    fmt: dict | None = None
    data_size: int | None = None
    chunk_count = 0
    pos = 12
    while pos < riff_end:
        if riff_end - pos < 8:
            raise SniffError("truncated_chunk",
                             "fewer than 8 bytes remain for a chunk header",
                             offset=pos, path="riff")
        chunk_count += 1
        if chunk_count > MAX_WAV_CHUNKS:
            raise SniffError("chunk_limit", f"more than {MAX_WAV_CHUNKS} chunks",
                             offset=pos, path="riff")
        fourcc = reader.read_at(pos, 4, path="riff")
        size = int.from_bytes(reader.read_at(pos + 4, 4, path="riff"), "little")
        body_start = pos + 8
        body_end = body_start + size
        name = fourcc_str(fourcc)
        pad = size & 1
        if body_end > riff_end:
            raise SniffError("out_of_bounds",
                             f"chunk {name!r} ends at {body_end}, past RIFF end {riff_end}",
                             offset=pos, path=f"riff/{name}")
        next_pos = body_end + pad
        if next_pos > riff_end:
            ctx.warn("missing_pad",
                     f"chunk {name!r} needs a pad byte past the RIFF end",
                     offset=pos, path=f"riff/{name}")
            next_pos = riff_end
        if fourcc == b"fmt ":
            if fmt is not None:
                ctx.warn("duplicate_chunk", "second fmt chunk ignored",
                         offset=pos, path="riff/fmt ")
            else:
                fmt = _parse_wav_fmt(reader, body_start, size, pos)
        elif fourcc == b"data":
            if data_size is not None:
                ctx.warn("duplicate_chunk", "second data chunk ignored",
                         offset=pos, path="riff/data")
            else:
                data_size = size
                data_start = body_start
        elif fourcc in (b"LIST", b"fact", b"bext", b"cue ", b"smpl", b"junk"):
            pass
        else:
            ctx.warn("unknown_chunk", f"unhandled RIFF chunk {name!r} "
                     f"({size} bytes)", offset=pos, path=f"riff/{name}")
        pos = next_pos

    if fmt is None:
        raise SniffError("missing_fmt", "no fmt chunk; audio parameters unavailable",
                         offset=12, path="riff")
    if data_size is None:
        raise SniffError("missing_data", "no data chunk; duration unavailable",
                         offset=12, path="riff")

    tag = fmt["tag"]
    block_align = fmt["block_align"]
    sample_rate = fmt["sample_rate"]
    channels = fmt["channels"]
    reasons: dict[str, str] = {}
    codec = WAV_FORMAT_NAMES.get(tag)
    if tag not in WAV_SUPPORTED_TAGS:
        ctx.warn("unsupported_codec",
                 f"WAV format tag 0x{tag:04x} is not supported", offset=0,
                 path="riff/fmt ")
        codec = f"wav-0x{tag:04x}"
    if sample_rate <= 0:
        reasons["sample_rate"] = "fmt chunk sample rate is zero"
    if channels <= 0:
        reasons["channels"] = "fmt chunk channel count is zero"
    if block_align <= 0:
        reasons["duration"] = "fmt chunk block align is zero; duration cannot be derived"
        duration_units = None
    else:
        duration_units = data_size // block_align

    track = SniffTrack(
        track_id=1, handler="audio", timescale=sample_rate if sample_rate > 0 else None,
        duration_units=duration_units, start_time_units=0, codec=codec,
        codec_raw=f"0x{tag:04x}", language=None, width=None, height=None,
        sample_rate=sample_rate if sample_rate > 0 else None,
        channels=channels if channels > 0 else None,
        bit_depth=fmt["bits_per_sample"] or None,
        reasons=reasons,
    )
    if track.sample_rate is None:
        track.reasons.setdefault("sample_rate", "fmt chunk sample rate is zero")
    if track.channels is None:
        track.reasons.setdefault("channels", "fmt chunk channel count is zero")
    if track.duration_units is None:
        track.reasons.setdefault("duration", "data size or block align unavailable")

    _ = data_start  # data payload is intentionally not read
    return SniffResult(
        container="wav", file_size=reader.size, brands=[],
        movie_timescale=sample_rate if sample_rate > 0 else None,
        movie_duration_units=duration_units, tracks=[track],
        warnings=warnings, wav_format_tag=fmt["declared_tag"],
    )


def _parse_wav_fmt(reader: Reader, body_start: int, size: int,
                   chunk_start: int) -> dict:
    if size < 16:
        raise SniffError("truncated_chunk",
                         f"fmt chunk is {size} bytes, minimum is 16",
                         offset=chunk_start, path="riff/fmt ")
    raw = reader.read_at(body_start, min(size, 64), path="riff/fmt ")
    tag = int.from_bytes(raw[0:2], "little")
    channels = int.from_bytes(raw[2:4], "little")
    sample_rate = int.from_bytes(raw[4:8], "little")
    block_align = int.from_bytes(raw[12:14], "little")
    bits = int.from_bytes(raw[14:16], "little")
    declared_tag = tag
    if tag == 0xFFFE:
        # WAVE_FORMAT_EXTENSIBLE carries the real codec in the first two
        # bytes of the SubFormat GUID.
        if len(raw) < 26:
            raise SniffError("truncated_chunk",
                             "WAVE_FORMAT_EXTENSIBLE fmt chunk is truncated",
                             offset=chunk_start, path="riff/fmt ")
        tag = int.from_bytes(raw[24:26], "little")
    return {
        "tag": tag, "declared_tag": declared_tag, "channels": channels,
        "sample_rate": sample_rate, "block_align": block_align,
        "bits_per_sample": bits,
    }


# ---------------------------------------------------------------------------
# Dispatch
# ---------------------------------------------------------------------------

def sniff_path(path) -> SniffResult:
    """Open a local file, detect its container and return a SniffResult.

    Raises SniffError for structural problems and OSError for I/O problems;
    callers turn both into per-file failures.
    """
    from pathlib import Path

    p = Path(path)
    size = p.stat().st_size
    if size > MAX_FILE_BYTES:
        raise SniffError("file_too_large",
                         f"{p} is {size} bytes; limit is {MAX_FILE_BYTES}",
                         offset=0, path="")
    if size < 12:
        raise SniffError("file_too_small",
                         f"{p} is {size} bytes; too small for any supported container",
                         offset=0, path="")
    ctx = SniffContext()
    with p.open("rb") as handle:
        reader = Reader(handle, size)
        magic = reader.read_at(0, 12, path="")
        if magic[4:8] == FTYP:
            try:
                return sniff_mp4(reader, ctx)
            except SniffError as exc:
                exc.file_size = size
                raise
        if magic[0:4] == b"RIFF" and magic[8:12] == b"WAVE":
            try:
                return sniff_wav(reader, ctx)
            except SniffError as exc:
                exc.file_size = size
                raise
        raise SniffError(
            "unknown_container",
            f"unrecognized signature {magic!r}; expected ftyp or RIFF/WAVE",
            offset=0, path="")
