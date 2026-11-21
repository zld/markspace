"""Shared integer helpers for MP4 box construction.

Kept separate from :mod:`mkvmp4.mp4` so the temporal structure (boxes, sample
tables) can be reasoned about independently of Matroska adaptation.
"""

from __future__ import annotations

from dataclasses import dataclass, field

from .errors import OutputError


@dataclass
class Box:
    """A fully encoded ISOBMFF box: ``size|type|payload``."""

    box_type: str
    payload: bytes

    @property
    def box_type_bytes(self) -> bytes:
        raw = self.box_type.encode("latin-1")
        if len(raw) != 4:
            raise OutputError(f"box type {self.box_type!r} is not 4 bytes")
        return raw

    def encode(self) -> bytes:
        body = self.box_type_bytes + self.payload
        size = len(body) + 4
        if size > 0xFFFFFFFF:
            raise OutputError(
                f"box {self.box_type} is {size} bytes and needs a 64-bit size field"
            )
        return size.to_bytes(4, "big") + body


def box(box_type: str, *children: bytes) -> bytes:
    """Encode a container box from already-encoded children."""
    return Box(box_type, b"".join(children)).encode()


def full_box(box_type: str, version: int, flags: int, payload: bytes = b"") -> bytes:
    """Encode a full box: version byte + 24-bit flags + payload."""
    if not 0 <= version <= 0xFF:
        raise OutputError(f"box {box_type}: version {version} out of range")
    if not 0 <= flags <= 0xFFFFFF:
        raise OutputError(f"box {box_type}: flags {flags} out of range")
    header = bytes([version]) + flags.to_bytes(3, "big")
    return Box(box_type, header + payload).encode()


def u8(value: int) -> bytes:
    return _uint(value, 1, 0xFF)


def u16(value: int) -> bytes:
    return _uint(value, 2, 0xFFFF)


def u24(value: int) -> bytes:
    return _uint(value, 3, 0xFFFFFF)


def u32(value: int) -> bytes:
    return _uint(value, 4, 0xFFFFFFFF)


def u64(value: int) -> bytes:
    return _uint(value, 8, 0xFFFFFFFFFFFFFFFF)


def _uint(value: int, width: int, maximum: int) -> bytes:
    if not isinstance(value, int) or isinstance(value, bool):
        raise OutputError(f"expected an integer, got {type(value).__name__}")
    if value < 0:
        raise OutputError(f"negative value {value} cannot be encoded as u{width * 8}")
    if value > maximum:
        raise OutputError(f"value {value} does not fit in u{width * 8}")
    return value.to_bytes(width, "big")


def fourcc(code: str) -> bytes:
    raw = code.encode("latin-1")
    if len(raw) > 4:
        raise OutputError(f"fourcc {code!r} longer than 4 bytes")
    return raw.ljust(4, b" ")


MATRIX_IDENTITY = (
    u32(0x00010000) + u32(0) + u32(0)
    + u32(0) + u32(0x00010000) + u32(0)
    + u32(0) + u32(0) + u32(0x40000000)
)


# ---------------------------------------------------------------------------
# Sample entries (codec configuration recorded from Matroska CodecPrivate)
# ---------------------------------------------------------------------------

SUPPORTED_MP4_CODECS: dict[str, str] = {
    "V_MPEG4/ISO/AVC": "avc1",
    "A_AAC": "mp4a",
    "S_TEXT/UTF8": "tx3g",
}


def _descriptor_length(length: int) -> bytes:
    """MPEG-4 descriptor length: 7 bits per byte, high bit = continuation."""
    if length < 0:
        raise OutputError("descriptor length must be non-negative")
    chunks = [length & 0x7F]
    length >>= 7
    while length:
        chunks.append(0x80 | (length & 0x7F))
        length >>= 7
    return bytes(reversed(chunks))


def _descriptor(tag: int, payload: bytes) -> bytes:
    return bytes([tag]) + _descriptor_length(len(payload)) + payload


def _esds_payload(audio_specific_config: bytes, channels: int, sample_rate: int) -> bytes:
    if not audio_specific_config:
        raise OutputError("AAC track has no CodecPrivate (AudioSpecificConfig)")
    object_type = 0x40  # MPEG-4 audio
    stream_type = 0x15  # audio stream
    decoder_specific = _descriptor(0x05, audio_specific_config)
    decoder_config = (
        bytes([object_type, stream_type])
        + u24(0)  # bufferSizeDB
        + u32(0)  # maxBitrate (unknown, not fabricated)
        + u32(0)  # avgBitrate
        + decoder_specific
    )
    sl_config = _descriptor(0x06, b"\x02")
    es = (
        u16(1)  # ES_ID
        + b"\x00"  # flags
        + _descriptor(0x04, decoder_config)
        + sl_config
    )
    return _descriptor(0x03, es)


def _visual_sample_entry(
    *,
    width: int,
    height: int,
    codec_box: bytes,
) -> bytes:
    compressor = b"\x00" * 32
    body = (
        b"\x00" * 6  # reserved
        + u16(1)  # data_reference_index
        + b"\x00" * 16  # pre_defined / reserved
        + u16(width)
        + u16(height)
        + u32(0x00480000)  # 72 dpi
        + u32(0x00480000)
        + u32(0)
        + u16(1)  # frame_count
        + compressor
        + u16(0x0018)  # depth
        + (0xFFFF).to_bytes(2, "big", signed=False)  # pre_defined = -1
        + codec_box
    )
    return Box("avc1", body).encode()


def _audio_sample_entry(
    *, channels: int, sample_rate: int, esds: bytes
) -> bytes:
    body = (
        b"\x00" * 6
        + u16(1)
        + b"\x00" * 8
        + u16(channels)
        + u16(16)  # samplesize
        + u16(0)
        + u16(0)
        + u32(sample_rate << 16)
        + esds
    )
    return Box("mp4a", body).encode()


def _tx3g_sample_entry(width: int, height: int) -> bytes:
    body = (
        b"\x00" * 6
        + u16(1)
        + u32(0)  # displayFlags
        + b"\x01"  # horizontal justification: center
        + b"\xff"  # vertical justification: bottom
        + b"\x00\x00\x00\x00"  # background colour
        + b"\x00\x00\x00\x00\x00\x00\x00\x00"  # BoxRecord
        + u16(0) + u16(0) + u16(1) + b"\x00" + u8(12) + b"\xff\xff\xff\xff"
    )
    del width, height  # tx3g has no fixed raster dimensions
    return Box("tx3g", body).encode()


def build_sample_entry(
    codec_id: str,
    *,
    codec_private: bytes,
    width: int = 0,
    height: int = 0,
    channels: int = 2,
    sample_rate: int = 48000,
) -> bytes:
    """Build the MP4 sample entry for a supported Matroska codec.

    Configuration is copied verbatim where MP4 and Matroska share the same
    record (AVC) and wrapped where a descriptor is required (AAC).  Nothing is
    transcoded.
    """
    entry = SUPPORTED_MP4_CODECS.get(codec_id)
    if entry is None:
        raise OutputError(f"codec {codec_id!r} has no MP4 sample entry mapping")
    if entry == "avc1":
        if len(codec_private) < 7 or codec_private[0] != 1:
            raise OutputError(
                "AVC CodecPrivate is not a valid AVCDecoderConfigurationRecord"
            )
        return _visual_sample_entry(
            width=width, height=height, codec_box=Box("avcC", codec_private).encode()
        )
    if entry == "mp4a":
        return _audio_sample_entry(
            channels=channels,
            sample_rate=sample_rate,
            esds=full_box("esds", 0, 0, _esds_payload(codec_private, channels, sample_rate)),
        )
    if entry == "tx3g":
        return _tx3g_sample_entry(width, height)
    raise OutputError(f"unhandled sample entry {entry}")


# ---------------------------------------------------------------------------
# Sample tables
# ---------------------------------------------------------------------------


def run_length(values: list[int]) -> list[tuple[int, int]]:
    """Collapse a sequence into (count, value) runs, preserving order."""
    entries: list[tuple[int, int]] = []
    for value in values:
        if value < 0:
            raise OutputError(f"sample table value {value} is negative")
        if entries and entries[-1][1] == value:
            entries[-1] = (entries[-1][0] + 1, value)
        else:
            entries.append((1, value))
    return entries


def build_stts(durations: list[int]) -> bytes:
    if not durations or any(d <= 0 for d in durations):
        raise OutputError("stts requires at least one positive duration")
    entries = run_length(durations)
    payload = u32(len(entries)) + b"".join(
        u32(count) + u32(delta) for count, delta in entries
    )
    return full_box("stts", 0, 0, payload)


def build_stsz(sizes: list[int]) -> bytes:
    if not sizes:
        raise OutputError("stsz requires at least one sample")
    if any(s <= 0 for s in sizes):
        raise OutputError("stsz rejects zero-length samples")
    if len(set(sizes)) == 1:
        return full_box("stsz", 0, 0, u32(sizes[0]) + u32(len(sizes)))
    payload = u32(0) + u32(len(sizes)) + b"".join(u32(s) for s in sizes)
    return full_box("stsz", 0, 0, payload)


def build_stsc(samples_per_chunk: list[int]) -> bytes:
    """One chunk per entry-less run; entries must be contiguous chunks 1..N."""
    if not samples_per_chunk:
        raise OutputError("stsc requires at least one chunk")
    if any(s <= 0 for s in samples_per_chunk):
        raise OutputError("stsc rejects empty chunks")
    entries: list[tuple[int, int]] = []
    for index, count in enumerate(samples_per_chunk, start=1):
        if entries and entries[-1][1] == count:
            continue
        entries.append((index, count))
    payload = u32(len(entries)) + b"".join(
        u32(first) + u32(count) + u32(1) for first, count in entries
    )
    return full_box("stsc", 0, 0, payload)


def build_stco_chunk(offsets: list[int], *, force_co64: bool = False) -> bytes:
    if not offsets:
        raise OutputError("chunk offset table requires at least one chunk")
    if force_co64 or any(o > 0xFFFFFFFF for o in offsets):
        for offset in offsets:
            if offset < 0 or offset > 0xFFFFFFFFFFFFFFFF:
                raise OutputError(f"chunk offset {offset} does not fit in 64 bits")
        payload = u32(len(offsets)) + b"".join(u64(o) for o in offsets)
        return full_box("co64", 0, 0, payload)
    payload = u32(len(offsets)) + b"".join(u32(o) for o in offsets)
    return full_box("stco", 0, 0, payload)


def build_stss(sync_samples: list[int]) -> bytes | None:
    """Sync sample numbers (1-based).  ``None`` when every sample is a sync."""
    if not sync_samples:
        return None
    payload = u32(len(sync_samples)) + b"".join(u32(n) for n in sync_samples)
    return full_box("stss", 0, 0, payload)


# ---------------------------------------------------------------------------
# Movie assembly
# ---------------------------------------------------------------------------

# ISO-639-2/T packed into a 15-bit mdhd field (each letter minus 0x60).
_LANGUAGE_UND = 0x55C4


def encode_language(tag: str | None) -> int:
    """Pack a three-letter ISO-639-2 code; anything else becomes ``und``."""
    if not tag:
        return _LANGUAGE_UND
    code = tag.lower()
    if len(code) != 3 or not all("a" <= c <= "z" for c in code):
        return _LANGUAGE_UND
    return ((ord(code[0]) - 0x60) << 10) | ((ord(code[1]) - 0x60) << 5) | (ord(code[2]) - 0x60)


def decode_language(packed: int) -> str:
    letters = [((packed >> shift) & 0x1F) + 0x60 for shift in (10, 5, 0)]
    try:
        text = "".join(chr(c) for c in letters)
    except ValueError:
        return "und"
    return text if text.isalpha() else "und"


@dataclass
class TrackSpec:
    """Everything needed to encode one ``trak`` box."""

    track_id: int
    source_track: int
    handler: str  # vide | soun | sbtl
    timescale: int
    sample_entry: bytes
    durations: list[int]
    sizes: list[int]
    keyframes: list[bool]
    chunk_offset: int
    first_tick: int = 0
    language: str | None = None
    width: int = 0
    height: int = 0
    channels: int = 2
    sample_rate: int = 48000

    @property
    def media_duration(self) -> int:
        return sum(self.durations)


def build_ftyp(brand: str = "isom", compatible: tuple[str, ...] = ("isom", "iso2", "avc1", "mp41")) -> bytes:
    payload = fourcc(brand) + u32(0x200) + b"".join(fourcc(c) for c in compatible)
    return Box("ftyp", payload).encode()


def build_mvhd(movie_timescale: int, duration: int, next_track_id: int) -> bytes:
    payload = (
        u32(0) + u32(0)  # creation/modification time (no fabricated clock data)
        + u32(movie_timescale)
        + u32(duration)
        + u32(0x00010000)  # rate 1.0
        + u16(0x0100)  # volume 1.0
        + b"\x00\x00"
        + b"\x00" * 8
        + MATRIX_IDENTITY
        + b"\x00" * 24
        + u32(next_track_id)
    )
    return full_box("mvhd", 0, 0, payload)


def build_tkhd(
    track_id: int,
    duration: int,
    *,
    width: int = 0,
    height: int = 0,
    volume: float = 0.0,
) -> bytes:
    flags = 0x000007  # enabled | in movie | in preview
    payload = (
        u32(0) + u32(0)
        + u32(track_id)
        + u32(0)
        + u32(duration)
        + b"\x00" * 8
        + u16(0)  # layer
        + u16(0)  # alternate_group
        + u16(round(volume * 0x0100))
        + u16(0)
        + MATRIX_IDENTITY
        + u32(width << 16)
        + u32(height << 16)
    )
    return full_box("tkhd", 0, flags, payload)


def build_mdhd(timescale: int, duration: int, language: str | None) -> bytes:
    payload = (
        u32(0) + u32(0)
        + u32(timescale)
        + u32(duration)
        + u16(encode_language(language))
        + u16(0)
    )
    return full_box("mdhd", 0, 0, payload)


_HANDLER_NAMES = {
    "vide": "VideoHandler",
    "soun": "SoundHandler",
    "sbtl": "SubtitleHandler",
}


def build_hdlr(handler: str) -> bytes:
    name = _HANDLER_NAMES.get(handler, "Handler")
    payload = u32(0) + fourcc(handler) + b"\x00" * 12 + name.encode("utf-8") + b"\x00"
    return full_box("hdlr", 0, 0, payload)


def build_dinf() -> bytes:
    url = full_box("url ", 0, 0x000001)  # media in this file
    dref = full_box("dref", 0, 0, u32(1) + url)
    return box("dinf", dref)


def build_media_header(handler: str) -> bytes:
    if handler == "vide":
        return full_box("vmhd", 0, 1, u16(0) + u16(0) + u16(0) + u16(0))
    if handler == "soun":
        return full_box("smhd", 0, 0, u16(0) + u16(0))
    return full_box("nmhd", 0, 0)


def build_stbl(spec: TrackSpec) -> bytes:
    sync = [i + 1 for i, key in enumerate(spec.keyframes) if key]
    stss = build_stss(sync) if len(sync) != len(spec.keyframes) else None
    children = [build_stts(spec.durations), build_stsz(spec.sizes)]
    if stss is not None:
        children.append(stss)
    children.append(build_stsc([len(spec.sizes)]))
    children.append(build_stco_chunk([spec.chunk_offset]))
    return box("stbl", *children)


def build_elst(empty_edit: int, media_duration: int) -> bytes:
    """Edit list expressing the first-sample delay, in movie timescale.

    ``stts`` deltas only describe spacing between samples; MP4 media time
    starts at zero.  A leading empty edit delays the media so the first
    presentation tick recorded by the timestamp stage is preserved exactly.
    """
    payload = u32(2)
    payload += u32(empty_edit) + u32(0xFFFFFFFF) + u32(0x00010000)  # empty edit
    payload += u32(media_duration) + u32(0) + u32(0x00010000)  # media at t=0
    return full_box("elst", 0, 0, payload)


def build_trak(spec: TrackSpec, movie_timescale: int) -> bytes:
    """Encode one track, including the edit list that positions sample 0."""
    media_duration = spec.media_duration
    empty_edit = 0
    if spec.first_tick > 0:
        empty_edit = spec.first_tick * movie_timescale // spec.timescale
        if empty_edit * spec.timescale != spec.first_tick * movie_timescale:
            raise OutputError(
                f"track {spec.track_id}: first tick {spec.first_tick} cannot be "
                f"represented exactly at movie timescale {movie_timescale}"
            )
    tkhd = build_tkhd(
        spec.track_id,
        empty_edit + media_duration,
        width=spec.width,
        height=spec.height,
        volume=0.0 if spec.handler == "vide" else 1.0,
    )
    mdhd = build_mdhd(spec.timescale, media_duration, spec.language)
    minf = box(
        "minf", build_media_header(spec.handler), build_dinf(), build_stbl(spec)
    )
    mdia = box("mdia", mdhd, build_hdlr(spec.handler), minf)
    if empty_edit:
        return box("trak", tkhd, box("edts", build_elst(empty_edit, media_duration)), mdia)
    return box("trak", tkhd, mdia)


def build_moov(*, movie_timescale: int, tracks: list[TrackSpec]) -> bytes:
    """Assemble ``moov``; chunk offsets in ``tracks`` must already be final."""
    duration = max(
        (
            t.first_tick * movie_timescale // t.timescale
            + t.media_duration * movie_timescale // t.timescale
            for t in tracks
        ),
        default=0,
    )
    next_track_id = max((t.track_id for t in tracks), default=0) + 1
    mvhd = build_mvhd(movie_timescale, duration, next_track_id)
    return box("moov", mvhd, *(build_trak(t, movie_timescale) for t in tracks))


def mdat_header(payload_size: int) -> bytes:
    if payload_size < 0:
        raise OutputError("mdat payload size cannot be negative")
    if payload_size + 8 <= 0xFFFFFFFF:
        return u32(payload_size + 8) + b"mdat"
    # 64-bit extended size: size field == 1, then a 64-bit size after the type.
    if payload_size + 16 > 0xFFFFFFFFFFFFFFFF:
        raise OutputError("mdat payload exceeds the 64-bit box size limit")
    return u32(1) + b"mdat" + u64(payload_size + 16)


# ---------------------------------------------------------------------------
# Verification: re-read the written file and check the index against the data
# ---------------------------------------------------------------------------

@dataclass
class ParsedBox:
    box_type: str
    offset: int
    size: int
    header_size: int
    payload_offset: int

    @property
    def end(self) -> int:
        return self.offset + self.size


@dataclass
class Mp4Inspection:
    boxes: list[ParsedBox]
    tracks: list[dict[str, object]]
    failures: list[str]
    sample_offsets: list[int]
    file_size: int

    @property
    def ok(self) -> bool:
        return not self.failures

    def to_dict(self) -> dict[str, object]:
        return {
            "file_size": self.file_size,
            "box_count": len(self.boxes),
            "boxes": [
                {"type": b.box_type, "offset": b.offset, "size": b.size}
                for b in self.boxes
            ],
            "tracks": self.tracks,
            "sample_count": sum(int(t["sample_count"]) for t in self.tracks),
            "index_ok": not any("offset" in f or "index" in f for f in self.failures),
            "failures": self.failures,
        }


def _read_box(data: bytes, pos: int, limit: int, path: str) -> ParsedBox:
    if pos + 8 > limit:
        raise OutputError(f"{path}: truncated box header at {pos}")
    size = int.from_bytes(data[pos : pos + 4], "big")
    box_type = data[pos + 4 : pos + 8].decode("latin-1")
    header = 8
    if size == 1:
        if pos + 16 > limit:
            raise OutputError(f"{path}/{box_type}: truncated 64-bit box size")
        size = int.from_bytes(data[pos + 8 : pos + 16], "big")
        header = 16
    elif size == 0:
        size = limit - pos
    if size < header:
        raise OutputError(f"{path}/{box_type}: box size {size} smaller than its header")
    if pos + size > limit:
        raise OutputError(
            f"{path}/{box_type}: box at {pos} declares size {size} beyond {limit}"
        )
    return ParsedBox(box_type, pos, size, header, pos + header)


def _iter_boxes(data: bytes, start: int, limit: int, path: str) -> list[ParsedBox]:
    boxes: list[ParsedBox] = []
    pos = start
    while pos < limit:
        parsed = _read_box(data, pos, limit, path)
        boxes.append(parsed)
        pos = parsed.end
    if pos != limit:
        raise OutputError(f"{path}: child boxes do not fill their parent exactly")
    return boxes


def _children(data: bytes, parent: ParsedBox, path: str) -> list[ParsedBox]:
    return _iter_boxes(data, parent.payload_offset, parent.end, f"{path}/{parent.box_type}")


def _find(boxes: list[ParsedBox], box_type: str) -> ParsedBox | None:
    return next((b for b in boxes if b.box_type == box_type), None)


def _table_entries(data: bytes, b: ParsedBox, words: int) -> list[int]:
    """Read a full-box table: u32 entry count then ``words`` u32 per entry."""
    payload = b.payload_offset
    count = int.from_bytes(data[payload + 4 : payload + 8], "big")
    total = count * words
    expected = 8 + 4 * total
    if b.size - b.header_size != expected:
        raise OutputError(
            f"table box {b.box_type} at {b.offset} declares {count} entries but "
            f"occupies {b.size - b.header_size} payload bytes (expected {expected})"
        )
    return [
        int.from_bytes(data[payload + 8 + 4 * i : payload + 12 + 4 * i], "big")
        for i in range(total)
    ]


def _parse_stbl(data: bytes, stbl: ParsedBox, path: str, failures: list[str]) -> dict[str, object]:
    kids = _children(data, stbl, path)
    info: dict[str, object] = {}
    stsz = _find(kids, "stsz")
    if stsz is None:
        failures.append(f"{path}: stbl has no stsz")
        return info
    p = stsz.payload_offset
    sample_size = int.from_bytes(data[p + 4 : p + 8], "big")
    count = int.from_bytes(data[p + 8 : p + 12], "big")
    if sample_size:
        sizes = [sample_size] * count
        expected = 12
    else:
        sizes = [
            int.from_bytes(data[p + 12 + 4 * i : p + 16 + 4 * i], "big")
            for i in range(count)
        ]
        expected = 12 + 4 * count
    if stsz.size - stsz.header_size != expected:
        failures.append(f"{path}: stsz size does not match its sample count")
    if any(s == 0 for s in sizes):
        failures.append(f"{path}: stsz contains a zero-length sample")
    info["sample_count"] = count
    info["sample_sizes"] = sizes

    stts = _find(kids, "stts")
    if stts is None:
        failures.append(f"{path}: stbl has no stts")
    else:
        values = _table_entries(data, stts, 2)
        if len(values) % 2:
            failures.append(f"{path}: stts entry count is not a multiple of 2")
        deltas: list[int] = []
        for i in range(0, len(values), 2):
            run, delta = values[i], values[i + 1]
            if delta == 0:
                failures.append(f"{path}: stts contains a zero delta")
            deltas.extend([delta] * run)
        if len(deltas) != count:
            failures.append(
                f"{path}: stts covers {len(deltas)} samples but stsz has {count}"
            )
        info["durations"] = deltas

    stsc = _find(kids, "stsc")
    if stsc is None:
        failures.append(f"{path}: stbl has no stsc")
    else:
        values = _table_entries(data, stsc, 3)
        if len(values) % 3:
            failures.append(f"{path}: stsc entry count is not a multiple of 3")
        entries = [
            (values[i], values[i + 1]) for i in range(0, len(values) - 2, 3)
        ]
        info["stsc"] = entries

    chunk_box = _find(kids, "stco")
    offset_width = 4
    if chunk_box is None:
        chunk_box = _find(kids, "co64")
        offset_width = 8
    if chunk_box is None:
        failures.append(f"{path}: stbl has no stco/co64")
        info["chunk_offsets"] = []
    else:
        p = chunk_box.payload_offset
        chunk_count = int.from_bytes(data[p + 4 : p + 8], "big")
        chunks = [
            int.from_bytes(
                data[p + 8 + offset_width * i : p + 8 + offset_width * (i + 1)], "big"
            )
            for i in range(chunk_count)
        ]
        if chunk_box.size - chunk_box.header_size != 8 + offset_width * chunk_count:
            failures.append(f"{path}: {chunk_box.box_type} size does not match its entry count")
        info["chunk_offsets"] = chunks
        info["chunk_box"] = chunk_box.box_type

    stss = _find(kids, "stss")
    if stss is not None:
        values = _table_entries(data, stss, 1)
        info["sync_samples"] = values
        for value in values:
            if value < 1 or value > count:
                failures.append(f"{path}: stss references sample {value} outside 1..{count}")

    info["stbl_children"] = [b.box_type for b in kids]
    return info


def inspect_mp4(data: bytes) -> Mp4Inspection:
    """Walk every box, rebuild the sample map and cross-check chunk offsets.

    This is deliberately independent of the writing code: it re-derives the
    sample positions from ``stsc``/``stco`` and confirms each one lands inside
    the ``mdat`` payload, that chunk offsets are ascending, and that the sum of
    sample sizes fits the file.
    """
    failures: list[str] = []
    boxes = _iter_boxes(data, 0, len(data), "/")
    if not boxes:
        raise OutputError("file contains no boxes")
    if boxes[0].box_type != "ftyp":
        failures.append("first box is not ftyp")
    moov = _find(boxes, "moov")
    mdat = _find(boxes, "mdat")
    if moov is None:
        raise OutputError("file has no moov box")
    if mdat is None:
        raise OutputError("file has no mdat box")
    mdat_start, mdat_end = mdat.payload_offset, mdat.end

    tracks: list[dict[str, object]] = []
    sample_offsets: list[int] = []
    moov_kids = _children(data, moov, "/moov")
    mvhd = _find(moov_kids, "mvhd")
    if mvhd is None:
        failures.append("/moov: no mvhd")
    for trak in (b for b in moov_kids if b.box_type == "trak"):
        trak_path = f"/moov/trak@{trak.offset}"
        trak_kids = _children(data, trak, trak_path)
        tkhd = _find(trak_kids, "tkhd")
        mdia = _find(trak_kids, "mdia")
        if tkhd is None or mdia is None:
            failures.append(f"{trak_path}: missing tkhd or mdia")
            continue
        track_id = int.from_bytes(
            data[tkhd.payload_offset + 12 : tkhd.payload_offset + 16], "big"
        )
        mdia_kids = _children(data, mdia, f"{trak_path}/mdia")
        hdlr = _find(mdia_kids, "hdlr")
        minf = _find(mdia_kids, "minf")
        mdhd = _find(mdia_kids, "mdhd")
        if hdlr is None or minf is None or mdhd is None:
            failures.append(f"{trak_path}: missing hdlr/minf/mdhd")
            continue
        handler = data[hdlr.payload_offset + 8 : hdlr.payload_offset + 12].decode(
            "latin-1"
        )
        timescale = int.from_bytes(
            data[mdhd.payload_offset + 12 : mdhd.payload_offset + 16], "big"
        )
        media_duration = int.from_bytes(
            data[mdhd.payload_offset + 16 : mdhd.payload_offset + 20], "big"
        )
        minf_kids = _children(data, minf, f"{trak_path}/minf")
        stbl = _find(minf_kids, "stbl")
        if stbl is None:
            failures.append(f"{trak_path}: minf has no stbl")
            continue
        info = _parse_stbl(data, stbl, f"{trak_path}/stbl", failures)
        sizes: list[int] = info.pop("sample_sizes", [])
        durations: list[int] = info.pop("durations", [])
        chunks: list[int] = info.pop("chunk_offsets", [])
        stsc: list[tuple[int, int]] = info.pop("stsc", [])
        # Expand stsc into samples-per-chunk for every chunk.
        per_chunk: list[int] = []
        if stsc:
            for index, (first, spc) in enumerate(stsc):
                last = stsc[index + 1][0] - 1 if index + 1 < len(stsc) else len(chunks)
                per_chunk.extend([spc] * (last - first + 1))
        if len(per_chunk) != len(chunks):
            failures.append(
                f"{trak_path}: stsc describes {len(per_chunk)} chunks but "
                f"stco/co64 has {len(chunks)}"
            )
        if sum(per_chunk) != len(sizes):
            failures.append(
                f"{trak_path}: chunk table maps {sum(per_chunk)} samples but "
                f"stsz has {len(sizes)}"
            )
        if chunks != sorted(chunks):
            failures.append(f"{trak_path}: chunk offsets are not strictly ascending")
        sample_index = 0
        for chunk_index, (offset, spc) in enumerate(zip(chunks, per_chunk)):
            cursor = offset
            for _ in range(spc):
                if sample_index >= len(sizes):
                    break
                size = sizes[sample_index]
                if cursor < mdat_start or cursor + size > mdat_end:
                    failures.append(
                        f"{trak_path}: sample {sample_index + 1} at byte {cursor} "
                        f"(len {size}) is outside mdat [{mdat_start}, {mdat_end})"
                    )
                sample_offsets.append(cursor)
                cursor += size
                sample_index += 1
            if chunk_index + 1 < len(chunks):
                following = chunks[chunk_index + 1]
                if cursor > following:
                    failures.append(
                        f"{trak_path}: chunk {chunk_index + 1} overlaps the next chunk"
                    )
        tracks.append(
            {
                "track_id": track_id,
                "handler": handler,
                "timescale": timescale,
                "media_duration": media_duration,
                "sample_count": len(sizes),
                "chunks": len(chunks),
                "chunk_box": info.get("chunk_box"),
                "duration_sum": sum(durations),
                "sync_samples": len(info.get("sync_samples", [])),
                "stbl_children": info.get("stbl_children"),
            }
        )
        if durations and sum(durations) != media_duration:
            failures.append(
                f"{trak_path}: stts duration sum {sum(durations)} != mdhd duration "
                f"{media_duration}"
            )
    return Mp4Inspection(
        boxes=boxes,
        tracks=tracks,
        failures=failures,
        sample_offsets=sample_offsets,
        file_size=len(data),
    )
