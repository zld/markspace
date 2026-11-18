"""ISO BMFF (MP4/MOV) field extraction.

Walks the box tree produced by :mod:`mediacheck.boxes` and turns
``mvhd``/``mdhd``/``hdlr``/``stsd`` leaves into unified track records.
Concerns here are metadata semantics; structural limits live in the box
walker.
"""

from __future__ import annotations

import hashlib
import struct

from . import __version__
from .boxes import Box, parse_box_tree, read_payload
from .model import MediaError, SniffResult, Track
from .util import MAX_TRACKS, normalise_language

_HANDLER_KIND = {
    "soun": "audio",
    "vide": "video",
    "text": "subtitle",
    "sbtl": "subtitle",
    "subt": "subtitle",
    "clcp": "subtitle",
}
_UNKNOWN_DURATION = 0xFFFFFFFF
_UNKNOWN_DURATION_64 = 0xFFFFFFFFFFFFFFFF


def parse_mp4(handle, size: int) -> SniffResult:
    try:
        tree = parse_box_tree(handle, size, strict=False)
    except MediaError as exc:
        return SniffResult(container="mp4", size=size, error=str(exc))

    warnings = list(tree.warnings)
    ftyp = tree.find("ftyp")
    metadata: dict = {"major_brand": None, "compatible_brands": []}
    if ftyp is not None:
        try:
            payload = read_payload(handle, ftyp, limit=4096)
        except MediaError as exc:
            return SniffResult(container="mp4", size=size, error=str(exc))
        if len(payload) >= 8:
            metadata["major_brand"] = payload[:4].decode("latin-1")
            (minor,) = struct.unpack(">I", payload[4:8])
            metadata["minor_version"] = minor
            brands = []
            for start in range(8, len(payload) - 3, 4):
                brands.append(payload[start:start + 4].decode("latin-1"))
            metadata["compatible_brands"] = brands
    else:
        warnings.append("missing ftyp box")

    moov = tree.find("moov")
    if moov is None:
        error = "missing moov box"
        if tree.truncated:
            error += " (file appears truncated)"
        return SniffResult(
            container="mp4",
            size=size,
            warnings=warnings,
            error=error,
            truncated=tree.truncated,
            metadata=metadata,
        )

    declared_duration = _movie_duration(handle, moov, warnings)
    traks = moov.find_all("trak")
    if len(traks) > MAX_TRACKS:
        return SniffResult(
            container="mp4",
            size=size,
            warnings=warnings,
            error=(
                f"track count {len(traks)} exceeds limit {MAX_TRACKS} "
                f"(moov offset {moov.offset})"
            ),
            truncated=tree.truncated,
            metadata=metadata,
        )

    tracks: list[Track] = []
    for trak in traks:
        try:
            track = _parse_trak(handle, trak, warnings)
        except MediaError as exc:
            return SniffResult(
                container="mp4",
                size=size,
                tracks=tracks,
                warnings=warnings,
                error=str(exc),
                truncated=tree.truncated,
                metadata=metadata,
            )
        if track is not None:
            tracks.append(track)
    _assign_stable_indices(tracks)
    metadata["track_count"] = len(tracks)
    metadata["box_count"] = sum(1 for _ in tree.walk())
    metadata["tool_version"] = __version__
    return SniffResult(
        container="mp4",
        size=size,
        tracks=tracks,
        declared_duration_ms=declared_duration,
        warnings=warnings,
        error=None,
        truncated=tree.truncated,
        metadata=metadata,
    )


def _movie_duration(handle, moov: Box, warnings: list[str]) -> float | None:
    mvhd = moov.find("mvhd")
    if mvhd is None:
        warnings.append("missing mvhd box")
        return None
    try:
        payload = read_payload(handle, mvhd, limit=64)
    except MediaError:
        return None
    if len(payload) < 20:
        return None
    version = payload[0]
    if version == 1:
        if len(payload) < 32:
            return None
        (timescale,) = struct.unpack(">I", payload[20:24])
        (duration,) = struct.unpack(">Q", payload[24:32])
        unknown = duration == _UNKNOWN_DURATION_64
    else:
        (timescale,) = struct.unpack(">I", payload[12:16])
        (duration,) = struct.unpack(">I", payload[16:20])
        unknown = duration == _UNKNOWN_DURATION
    if not timescale or unknown:
        return None
    return round(duration / timescale * 1000.0, 3)


def _parse_trak(handle, trak: Box, warnings: list[str]) -> Track | None:
    mdia = trak.find("mdia")
    if mdia is None:
        warnings.append(f"trak at offset {trak.offset} has no mdia box")
        return None
    hdlr = mdia.find("hdlr")
    mdhd = mdia.find("mdhd")
    kind = "other"
    language = None
    if hdlr is not None:
        payload = read_payload(handle, hdlr, limit=64)
        if len(payload) >= 12:
            kind = _HANDLER_KIND.get(payload[8:12].decode("latin-1"), "other")
    timescale = None
    duration_units = None
    if mdhd is not None:
        timescale, duration_units, language = _parse_mdhd(
            read_payload(handle, mdhd, limit=64)
        )
    else:
        warnings.append(f"trak at offset {trak.offset} has no mdhd box")
    duration_ms = None
    if timescale and duration_units is not None:
        duration_ms = round(duration_units / timescale * 1000.0, 3)

    codec = None
    channel_count = sample_size = sample_rate = None
    codec_digest = None
    stbl = None
    minf = mdia.find("minf")
    if minf is not None:
        stbl = minf.find("stbl")
    stsd = stbl.find("stsd") if stbl is not None else None
    if stsd is not None:
        try:
            (
                codec,
                channel_count,
                sample_size,
                sample_rate,
                codec_digest,
            ) = _parse_stsd(handle, stsd, kind)
        except MediaError as exc:
            warnings.append(str(exc))
    else:
        warnings.append(f"trak at offset {trak.offset} has no stsd box")

    if kind == "audio" and sample_rate is None and timescale:
        sample_rate = timescale
    block_align = None
    if kind == "audio" and channel_count and sample_size:
        block_align = channel_count * sample_size // 8

    return Track(
        stable_index=0,
        kind=kind,
        language=language,
        codec=codec,
        duration_ms=duration_ms,
        sample_rate=sample_rate,
        channels=channel_count,
        bit_depth=sample_size,
        block_align=block_align,
        extra={
            "trak_offset": trak.offset,
            "timescale": timescale,
            "codec_digest": codec_digest,
        },
    )


def _parse_mdhd(payload: bytes) -> tuple[int | None, int | None, str | None]:
    if len(payload) < 24:
        return None, None, None
    version = payload[0]
    if version == 1:
        if len(payload) < 34:
            return None, None, None
        (timescale,) = struct.unpack(">I", payload[20:24])
        (duration,) = struct.unpack(">Q", payload[24:32])
        language = normalise_language(payload[32:34])
        unknown = duration == _UNKNOWN_DURATION_64
    else:
        (timescale,) = struct.unpack(">I", payload[12:16])
        (duration,) = struct.unpack(">I", payload[16:20])
        language = normalise_language(payload[20:22])
        unknown = duration == _UNKNOWN_DURATION
    return timescale or None, (None if unknown else duration), language


def _parse_stsd(
    handle, stsd: Box, kind: str
) -> tuple[str | None, int | None, int | None, int | None, str | None]:
    payload = read_payload(handle, stsd, limit=1 << 16)
    if len(payload) < 8:
        raise MediaError("stsd box too small", stsd.payload_offset)
    entry_count = struct.unpack(">I", payload[4:8])[0]
    if entry_count == 0:
        return None, None, None, None, None
    entries = _split_entries(payload[8:], stsd.payload_offset + 8)
    if not entries:
        raise MediaError("stsd declares no readable sample entry", stsd.payload_offset)
    entry = entries[0]
    entry_type = entry[4:8].decode("latin-1")
    body = entry[8:]
    digest = hashlib.sha256(entry).hexdigest()[:16]
    channels = sample_size = sample_rate = None
    if kind == "audio" and len(body) >= 28:
        channels = struct.unpack(">H", body[16:18])[0]
        sample_size = struct.unpack(">H", body[18:20])[0]
        rate_fixed = struct.unpack(">I", body[24:28])[0]
        sample_rate = rate_fixed >> 16
        if sample_rate == 0:
            sample_rate = None
    elif kind == "video" and len(body) >= 28:
        # width/height are informational only; the unified record keeps
        # no video geometry fields.
        pass
    return entry_type, channels, sample_size, sample_rate, digest


def _split_entries(data: bytes, base_offset: int) -> list[bytes]:
    entries: list[bytes] = []
    position = 0
    while position + 8 <= len(data):
        (entry_size,) = struct.unpack(">I", data[position:position + 4])
        if entry_size < 8 or position + entry_size > len(data):
            break
        entries.append(data[position:position + entry_size])
        position += entry_size
    return entries


def _assign_stable_indices(tracks: list[Track]) -> None:
    """Assign an ordinal that is stable under track reordering.

    The ordinal counts tracks sharing the same identity (kind, language,
    codec).  Reordering distinct tracks therefore never changes a stable
    index; tracks with identical identity are surfaced as ambiguous by the
    comparer instead of being silently merged.
    """
    counters: dict[tuple, int] = {}
    for track in tracks:
        key = (track.kind, track.language, track.codec)
        counters[key] = counters.get(key, 0) + 1
        track.stable_index = counters[key]
