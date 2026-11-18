"""Unified track/encoding metadata extracted from sniffed containers.

``extract_file`` is the single entry point reused by ``check`` and ``pack``:
it sniffs, parses, hashes and detects files that change mid-read.  Fields that
are absent from the container remain ``None`` and are listed in
``unknown_fields``; nothing is inferred from file extensions.
"""

from __future__ import annotations

import hashlib
import os
from dataclasses import dataclass
from typing import Any, BinaryIO

from . import bmff, sniff, wav
from .errors import (
    ERR_BOX_LENGTH,
    ERR_FILE_CHANGED,
    ERR_IO,
    ERR_PERMISSION,
    ERR_TRACK_LIMIT,
    ERR_UNKNOWN_FORMAT,
    ScanFailure,
)
from .limits import Limits

_HANDLER_TYPES = {
    "soun": "audio",
    "vide": "video",
    "text": "subtitle",
    "sbtl": "subtitle",
    "subt": "subtitle",
    "clcp": "subtitle",
    "hint": "hint",
    "tmcd": "timecode",
}
_AUDIO_ENTRIES = frozenset(
    {"mp4a", "alac", "ac-3", "ec-3", "Opus", "fLaC", ".mp3", "lpcm", "sowt",
     "twos", "samr", "sawb"}
)
_VIDEO_ENTRIES = frozenset(
    {"avc1", "avc3", "hvc1", "hev1", "mp4v", "vp08", "vp09", "av01", "ap4h",
     "apch", "jpeg", "png ", "raw "}
)
_AAC_RATES = [
    96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050,
    16000, 12000, 11025, 8000, 7350,
]
_OBJECT_TYPES = {
    0x40: "aac", 0x66: "aac", 0x67: "aac", 0x68: "aac",
    0x69: "mp3", 0x6B: "mp3", 0xA5: "ac3", 0xA6: "eac3",
    0xA9: "dts", 0xDD: "vorbis",
}


@dataclass
class Track:
    index: int
    type: str
    codec: str | None = None
    sample_rate: int | None = None
    channels: int | None = None
    bit_depth: int | None = None
    width: int | None = None
    height: int | None = None
    timescale: int | None = None
    duration_ms: int | None = None
    track_id: int | None = None
    handler: str | None = None

    def codec_summary(self) -> dict[str, Any]:
        if self.type == "audio":
            keys = ("codec", "sample_rate", "channels", "bit_depth")
        elif self.type == "video":
            keys = ("codec", "width", "height")
        else:
            keys = ("codec",)
        return {k: getattr(self, k) for k in keys}

    def unknown_fields(self) -> list[str]:
        return [k for k, v in self.codec_summary().items() if v is None]

    def to_dict(self) -> dict[str, Any]:
        return {
            "index": self.index,
            "track_id": self.track_id,
            "type": self.type,
            "handler": self.handler,
            "codec": self.codec,
            "sample_rate": self.sample_rate,
            "channels": self.channels,
            "bit_depth": self.bit_depth,
            "width": self.width,
            "height": self.height,
            "timescale": self.timescale,
            "duration_ms": self.duration_ms,
            "codec_summary": self.codec_summary(),
            "unknown_fields": self.unknown_fields(),
        }


@dataclass
class Record:
    path: str
    size_bytes: int
    sha256: str
    container: dict[str, Any]
    duration_ms: int | None
    tracks: list[Track]
    status: str = "ok"

    def unknown_fields(self) -> list[str]:
        return [] if self.duration_ms is not None else ["duration_ms"]

    def to_dict(self) -> dict[str, Any]:
        return {
            "path": self.path,
            "size_bytes": self.size_bytes,
            "sha256": self.sha256,
            "container": self.container,
            "duration_ms": self.duration_ms,
            "track_count": len(self.tracks),
            "tracks": [t.to_dict() for t in self.tracks],
            "unknown_fields": self.unknown_fields(),
            "status": self.status,
        }


def _stat(path: str) -> os.stat_result:
    try:
        return os.stat(path)
    except PermissionError as exc:
        raise ScanFailure(ERR_PERMISSION, f"permission denied: {path}", 0) from exc
    except OSError as exc:
        raise ScanFailure(ERR_IO, f"cannot stat file: {exc}", 0) from exc


def _hash(path: str, limits: Limits) -> str:
    digest = hashlib.sha256()
    try:
        with open(path, "rb") as fh:
            while True:
                chunk = fh.read(limits.max_chunk)
                if not chunk:
                    break
                digest.update(chunk)
    except PermissionError as exc:
        raise ScanFailure(ERR_PERMISSION, f"permission denied: {path}", 0) from exc
    except OSError as exc:
        raise ScanFailure(ERR_IO, f"cannot read file: {exc}", 0) from exc
    return digest.hexdigest()


def extract_file(
    path: str, limits: Limits | None = None, display_path: str | None = None
) -> Record:
    limits = limits or Limits()
    before = _stat(path)
    limits.check_file_size(before.st_size)
    result = sniff.sniff_path(path, limits)
    if result.kind in ("wav", "aiff"):
        record = _audio_record(path, result, limits, display_path)
    else:
        record = _bmff_record(path, result, limits, display_path)
    record.sha256 = _hash(path, limits)
    after = _stat(path)
    if (after.st_size, after.st_mtime_ns) != (before.st_size, before.st_mtime_ns):
        raise ScanFailure(ERR_FILE_CHANGED, "file changed while being read", 0)
    return record


def _audio_record(
    path: str, result: sniff.SniffResult, limits: Limits, display_path: str | None
) -> Record:
    data = (
        wav.parse_wav(path, limits)
        if result.kind == "wav"
        else wav.parse_aiff(path, limits)
    )
    track = Track(
        index=1,
        type="audio",
        codec=data.codec,
        sample_rate=data.sample_rate,
        channels=data.channels,
        bit_depth=data.bit_depth,
        timescale=data.sample_rate,
        duration_ms=data.container_duration_ms,
    )
    return Record(
        path=display_path or path,
        size_bytes=_stat(path).st_size,
        sha256="",
        container=result.as_dict(),
        duration_ms=data.container_duration_ms,
        tracks=[track],
    )


# -- ISO BMFF metadata ------------------------------------------------------


def _u32(raw: bytes, pos: int) -> int:
    return int.from_bytes(raw[pos : pos + 4], "big")


def _u64(raw: bytes, pos: int) -> int:
    return int.from_bytes(raw[pos : pos + 8], "big")


def _duration(fh: BinaryIO, box: bmff.Box) -> tuple[int | None, int | None]:
    """Read a versioned mvhd/mdhd payload -> (duration_ms, timescale)."""
    if box.payload_size < 20:
        return None, None
    raw = bmff.read_payload(fh, box, 32)
    if raw[0] == 1:
        if len(raw) < 32:
            return None, None
        scale, duration, sentinel = _u32(raw, 20), _u64(raw, 24), 0xFFFFFFFFFFFFFFFF
    else:
        scale, duration, sentinel = _u32(raw, 12), _u32(raw, 16), 0xFFFFFFFF
    if scale == 0:
        return None, None
    if duration == sentinel:
        return None, scale
    return round(duration * 1000 / scale), scale


def _bmff_record(
    path: str, result: sniff.SniffResult, limits: Limits, display_path: str | None
) -> Record:
    boxes, size = bmff.parse_file(path, limits)
    moov = next((b for b in boxes if b.type == "moov"), None)
    if moov is None:
        raise ScanFailure(ERR_UNKNOWN_FORMAT, "no moov box found", 0)
    traks = [b for b in moov.children if b.type == "trak"]
    if len(traks) > limits.max_tracks:
        raise ScanFailure(
            ERR_TRACK_LIMIT,
            f"{len(traks)} tracks exceed limit {limits.max_tracks}",
            moov.payload_offset,
        )
    try:
        with open(path, "rb") as fh:
            mvhd = _child(moov, "mvhd")
            duration_ms = None
            if mvhd is not None:
                duration_ms, _scale = _duration(fh, mvhd)
            tracks = [_track(fh, t, i + 1, limits) for i, t in enumerate(traks)]
    except OSError as exc:
        raise ScanFailure(ERR_IO, f"cannot read file: {exc}", 0) from exc
    return Record(
        path=display_path or path,
        size_bytes=size,
        sha256="",
        container=result.as_dict(),
        duration_ms=duration_ms,
        tracks=tracks,
    )


def _child(box: bmff.Box, name: str) -> bmff.Box | None:
    return next((b for b in box.children if b.type == name), None)


def _path(box: bmff.Box, *names: str) -> bmff.Box | None:
    current = box
    for name in names:
        found = _child(current, name)
        if found is None:
            return None
        current = found
    return current


def _track(fh: BinaryIO, trak: bmff.Box, index: int, limits: Limits) -> Track:
    tkhd = _child(trak, "tkhd")
    track_id = None
    if tkhd is not None and tkhd.payload_size >= 16:
        raw = bmff.read_payload(fh, tkhd, 24)
        offset = 20 if raw[0] == 1 else 12
        if len(raw) >= offset + 4:
            track_id = _u32(raw, offset)
    mdia = _child(trak, "mdia")
    handler = timescale = duration_ms = None
    if mdia is not None:
        hdlr = _child(mdia, "hdlr")
        if hdlr is not None and hdlr.payload_size >= 12:
            handler = bmff.read_payload(fh, hdlr, 12)[8:12].decode("latin-1")
        mdhd = _child(mdia, "mdhd")
        if mdhd is not None:
            duration_ms, timescale = _duration(fh, mdhd)
    track = Track(
        index=index,
        type=_HANDLER_TYPES.get(handler or "", "other"),
        timescale=timescale,
        duration_ms=duration_ms,
        track_id=track_id,
        handler=handler,
    )
    if mdia is not None:
        stsd = _path(mdia, "minf", "stbl", "stsd")
        if stsd is not None:
            _fill_sample_entry(fh, stsd, track, limits)
    return track


def _fill_sample_entry(
    fh: BinaryIO, stsd: bmff.Box, track: Track, limits: Limits
) -> None:
    if stsd.payload_size < 8:
        return
    count = _u32(bmff.read_payload(fh, stsd, 8), 4)
    pos = stsd.payload_offset + 8
    end = stsd.end
    for _ in range(min(count, limits.max_sample_entries)):
        if pos + 8 > end:
            break
        head = bmff.read_exact(fh, 8, pos)
        size = _u32(head, 0)
        fourcc = head[4:8].decode("latin-1")
        if size < 8 or pos + size > end:
            raise ScanFailure(
                ERR_BOX_LENGTH,
                f"sample entry {fourcc!r} size {size} out of bounds",
                pos,
            )
        body = bmff.read_exact(fh, min(size - 8, 160), pos + 8)
        if fourcc in _AUDIO_ENTRIES:
            _fill_audio_entry(fh, fourcc, body, pos + 8, pos + size, track)
        elif fourcc in _VIDEO_ENTRIES and len(body) >= 28:
            track.codec = fourcc.strip() or fourcc
            track.width = int.from_bytes(body[24:26], "big") or None
            track.height = int.from_bytes(body[26:28], "big") or None
        elif fourcc.strip():
            track.codec = fourcc.strip()
        pos += size


def _fill_audio_entry(
    fh: BinaryIO,
    fourcc: str,
    body: bytes,
    payload: int,
    end: int,
    track: Track,
) -> None:
    track.codec = fourcc.strip() or fourcc
    if len(body) < 28:
        return
    track.channels = int.from_bytes(body[16:18], "big") or None
    track.bit_depth = int.from_bytes(body[18:20], "big") or None
    rate = int.from_bytes(body[24:28], "big") >> 16
    track.sample_rate = rate or None
    if fourcc == "mp4a":
        version = int.from_bytes(body[8:10], "big")
        extra = 16 if version == 1 else 36 if version == 2 else 0
        _fill_esds(fh, payload + 28 + extra, end, track)


def _fill_esds(fh: BinaryIO, start: int, end: int, track: Track) -> None:
    pos = start
    while pos + 8 <= end:
        head = bmff.read_exact(fh, 8, pos)
        size = _u32(head, 0)
        btype = head[4:8]
        if size < 8 or pos + size > end:
            return
        if btype == b"esds" and size >= 12:
            desc = bmff.read_exact(fh, min(size - 12, 512), pos + 12)
            object_type, ascfg = _esds_object(desc)
            if object_type in _OBJECT_TYPES:
                track.codec = _OBJECT_TYPES[object_type]
            if ascfg:
                rate, channels = _audio_specific_config(ascfg)
                if rate:
                    track.sample_rate = rate
                if channels:
                    track.channels = channels
            return
        pos += size


def _desc_header(data: bytes, pos: int) -> tuple[int, int, int]:
    """Return (tag, payload_length, payload_start)."""
    if pos >= len(data):
        return -1, 0, pos
    tag = data[pos]
    pos += 1
    length = 0
    for _ in range(4):
        if pos >= len(data):
            return tag, length, pos
        byte = data[pos]
        pos += 1
        length = (length << 7) | (byte & 0x7F)
        if not byte & 0x80:
            break
    return tag, length, pos


def _esds_object(data: bytes) -> tuple[int | None, bytes]:
    """Walk ES_Descriptor -> DecoderConfigDescriptor -> DecoderSpecificInfo."""
    tag, length, pos = _desc_header(data, 0)
    if tag == 0x03:
        flags = data[pos + 2] if pos + 2 < len(data) else 0
        pos += 3
        if flags & 0x80:
            pos += 2
        if flags & 0x40 and pos < len(data):
            pos += 1 + data[pos]
        if flags & 0x20:
            pos += 2
        tag, length, pos = _desc_header(data, pos)
    if tag == 0x04:
        obj = data[pos] if pos < len(data) else None
        pos += 13
        tag, length, pos = _desc_header(data, pos)
        if tag == 0x05:
            return obj, data[pos : pos + length]
        return obj, b""
    # Some muxers write a flat descriptor list; scan for object type / ASC.
    for i in range(len(data) - 1):
        if data[i] == 0x05 and data[i + 1] < 0x80:
            return None, data[i + 2 : i + 2 + data[i + 1]]
    return None, b""


def _audio_specific_config(data: bytes) -> tuple[int | None, int | None]:
    if len(data) < 2:
        return None, None
    bits = int.from_bytes(data[:2], "big")
    object_type = (bits >> 11) & 0x1F
    if object_type == 31:
        return None, None
    index = (bits >> 7) & 0x0F
    rate = _AAC_RATES[index] if index < len(_AAC_RATES) else None
    channels = (bits >> 3) & 0x0F
    return rate, (channels or None)
