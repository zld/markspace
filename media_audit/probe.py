"""Unified, reusable metadata extractors for MP4/MOV and MKV files.

Failures are captured on the result (with byte offset) instead of aborting a
batch; the packaging entry point reuses this exact extraction code.
"""

from __future__ import annotations

import os
import struct
from dataclasses import dataclass, field
from typing import Any

from .ebml import EBMLError, EBMLReader
from .iso_bmff import (
    CONTAINER_BOXES,
    IsoBmffError,
    IsoBmffReader,
    META_BOX,
)

SUPPORTED_EXTENSIONS = (".mp4", ".mov", ".mkv")

# Probe-level cap independent of the structural limits in the box/EBML
# readers: even a structurally valid file cannot declare unlimited tracks.
DEFAULT_MAX_TRACKS = 512


def limits_max_tracks(limits: dict | None) -> int:
    if limits and "max_tracks" in limits:
        return int(limits["max_tracks"])
    return DEFAULT_MAX_TRACKS

# Normalised codec families used for cross-container comparisons.
_CODEC_FAMILY = {
    "avc1": "avc",
    "avc3": "avc",
    "hvc1": "hevc",
    "hev1": "hevc",
    "mp4v": "mp4v",
    "mp4a": "aac",
    "ac-3": "ac3",
    "ec-3": "eac3",
    "tx3g": "tx3g",
    "text": "text",
    "wvtt": "webvtt",
    "v_mpeg4/iso/avc": "avc",
    "v_mpegh/iso/hevc": "hevc",
    "v_ms/vfw/fourcc/divx": "mp4v",
    "a_aac": "aac",
    "a_ac3": "ac3",
    "a_e-ac3": "eac3",
    "a_ms/acm": "unknown",
    "s_text/utf8": "srt",
    "s_text/ass": "ass",
    "s_text/ssa": "ass",
    "s_text/webvtt": "webvtt",
}

_HANDLER_TYPES = {
    b"vide": "video",
    b"soun": "audio",
    b"subt": "subtitle",
    b"text": "subtitle",
    b"meta": "metadata",
}


@dataclass
class TrackInfo:
    """One track in a container, normalised across formats."""

    track_id: int | None
    type: str
    codec: str
    codec_family: str
    language: str | None
    hidden: bool
    params: dict[str, Any] = field(default_factory=dict)
    codec_summary: str = ""


@dataclass
class MediaInfo:
    """Extracted metadata for one container file."""

    path: str
    name: str
    container: str
    duration_ms: int | None = None
    brands: tuple[str, ...] = ()
    tracks: list[TrackInfo] = field(default_factory=list)
    warnings: list[str] = field(default_factory=list)
    parse_error: str | None = None
    error_offset: int | None = None
    byte_size: int = 0

    @property
    def ok(self) -> bool:
        return self.parse_error is None

    def track_counts(self) -> dict[str, int]:
        counts = {"video": 0, "audio": 0, "subtitle": 0, "other": 0}
        for track in self.tracks:
            if track.type in counts:
                counts[track.type] += 1
            else:
                counts["other"] += 1
        return counts


def codec_family_of(codec: str) -> str:
    return _CODEC_FAMILY.get(codec.strip().lower(), "unknown")


def _packed_iso_language(raw: int) -> str | None:
    """Decode an ISO-639-2/T language packed into three 5-bit fields."""
    if raw in (0, 0x55C0):  # 0x55C0 spells "und" (undetermined)
        return None
    chars = []
    for shift in (10, 5, 0):
        value = (raw >> shift) & 0x1F
        chars.append(chr(value + 0x60) if value else "")
    language = "".join(chars).strip()
    return language or None


def _find_children(reader: IsoBmffReader, start: int, end: int, wanted: set[bytes]):
    found: list = []
    for box in reader.iter_children(start, end):
        if box.type in wanted:
            found.append(box)
        if box.type in CONTAINER_BOXES:
            found.extend(_find_children(
                reader, box.payload_start, box.payload_end, wanted))
        elif box.type == META_BOX:
            found.extend(_find_children(
                reader, box.payload_start + 4, box.payload_end, wanted))
    return found


def _summarize(track: TrackInfo) -> str:
    if track.type == "video":
        width = track.params.get("width")
        height = track.params.get("height")
        dims = f" {width}x{height}" if width and height else ""
        return f"{track.codec_family}{dims}"
    if track.type == "audio":
        rate = track.params.get("sample_rate")
        channels = track.params.get("channels")
        bits = []
        if rate:
            bits.append(f"{int(rate)}Hz")
        if channels:
            bits.append(f"{channels}ch")
        return f"{track.codec_family} {' '.join(bits)}".strip()
    return track.codec_family


# ISO BMFF (MP4/MOV) probing


def _parse_mvhd(reader: IsoBmffReader, box) -> int | None:
    data = reader.payload(box, 128)
    version = data[0]
    if version == 1:
        timescale = struct.unpack_from(">I", data, 20)[0]
        duration = struct.unpack_from(">Q", data, 24)[0]
    else:
        timescale = struct.unpack_from(">I", data, 12)[0]
        duration = struct.unpack_from(">I", data, 16)[0]
    if timescale == 0:
        return None
    return round(duration * 1000 / timescale)


def _parse_trak(reader: IsoBmffReader, trak) -> TrackInfo:
    wanted = {b"tkhd", b"mdhd", b"hdlr", b"stsd"}
    children = {b.type: b for b in _find_children(
        reader, trak.payload_start, trak.payload_end, wanted)}
    track_id: int | None = None
    hidden = False
    if b"tkhd" in children:
        tkhd = reader.payload(children[b"tkhd"], 128)
        version = tkhd[0]
        flags = int.from_bytes(tkhd[1:4], "big")
        hidden = not bool(flags & 0x000001)  # track_enabled bit
        offset = 12 if version == 0 else 20
        (track_id,) = struct.unpack_from(">I", tkhd, offset)

    timescale = duration_raw = language_raw = None
    if b"mdhd" in children:
        mdhd = reader.payload(children[b"mdhd"], 64)
        version = mdhd[0]
        if version == 1:
            timescale, duration_raw = struct.unpack_from(">IQ", mdhd, 20)
            (language_raw,) = struct.unpack_from(">H", mdhd, 32)
        else:
            timescale, duration_raw = struct.unpack_from(">II", mdhd, 12)
            (language_raw,) = struct.unpack_from(">H", mdhd, 20)

    handler_type = b""
    if b"hdlr" in children:
        hdlr = reader.payload(children[b"hdlr"], 64)
        handler_type = hdlr[8:12]
    track_type = _HANDLER_TYPES.get(handler_type, "unknown")

    codec = ""
    params: dict[str, Any] = {}
    if b"stsd" in children:
        codec, params = _parse_stsd(reader, children[b"stsd"], track_type)
    if timescale:
        params["timescale"] = timescale
        if duration_raw is not None:
            params["duration_ms"] = round(duration_raw * 1000 / timescale)

    language = (
        _packed_iso_language(language_raw)
        if language_raw is not None else None
    )
    track = TrackInfo(
        track_id=track_id,
        type=track_type,
        codec=codec or "unknown",
        codec_family=codec_family_of(codec) if codec else "unknown",
        language=language,
        hidden=hidden,
        params=params,
    )
    track.codec_summary = _summarize(track)
    return track


def _parse_stsd(reader: IsoBmffReader, stsd, track_type: str):
    data = reader.payload(stsd, 4096)
    if len(data) < 8:
        raise IsoBmffError("stsd payload too short", stsd.payload_start)
    (entry_count,) = struct.unpack_from(">I", data, 4)  # skip version/flags
    if entry_count == 0:
        return "", {}
    # First sample entry starts at offset 8: 4-byte size, 4-byte type, then
    # the type-specific sample entry fields.
    entry = 8
    codec = data[entry + 4:entry + 8].decode("ascii", errors="replace")
    params: dict[str, Any] = {}
    if track_type == "video" and len(data) >= entry + 26:
        # VisualSampleEntry: 6 reserved + 2 dref + 2 pre_defined + 6 reserved,
        # then width/height as 16-bit integers.
        params["width"] = struct.unpack_from(">H", data, entry + 24)[0]
        params["height"] = struct.unpack_from(">H", data, entry + 26)[0]
    elif track_type == "audio" and len(data) >= entry + 36:
        # AudioSampleEntry: 6 reserved + 2 dref + 8 version/revision/vendor,
        # then channels, sample size and a 16.16 fixed-point sample rate.
        params["channels"] = struct.unpack_from(">H", data, entry + 24)[0]
        params["sample_size"] = struct.unpack_from(">H", data, entry + 26)[0]
        rate_fixed = struct.unpack_from(">I", data, entry + 32)[0]
        params["sample_rate"] = rate_fixed >> 16
    return codec, params


def probe_iso(path: str, container: str, limits=None) -> MediaInfo:
    info = MediaInfo(
        path=path,
        name=os.path.basename(path),
        container=container,
        byte_size=os.path.getsize(path),
    )
    try:
        with IsoBmffReader(path, limits) as reader:
            for box in reader.iter_children(0, reader.file_size):
                if box.type == b"ftyp":
                    payload = reader.payload(box, 4096)
                    decode = lambda b: b.decode("ascii", errors="replace")
                    brands = [decode(payload[0:4])] if len(payload) >= 4 else []
                    # Bytes 4..8 are the minor version; brands start at 8.
                    brands.extend(decode(payload[i:i + 4])
                                  for i in range(8, len(payload) - 3, 4))
                    info.brands = tuple(brands)
                elif box.type == b"moov":
                    for inner in reader.iter_children(
                        box.payload_start, box.payload_end
                    ):
                        if inner.type == b"mvhd":
                            info.duration_ms = _parse_mvhd(reader, inner)
                        elif inner.type == b"trak":
                            if len(info.tracks) >= limits_max_tracks(limits):
                                raise IsoBmffError(
                                    f"track count exceeds "
                                    f"{limits_max_tracks(limits)}", inner.start)
                            info.tracks.append(_parse_trak(reader, inner))
    except (IsoBmffError, struct.error, OSError) as exc:
        info.parse_error = str(exc)
        info.error_offset = getattr(exc, "offset", None)
    for track in info.tracks:
        track.codec_summary = _summarize(track)
    return info

