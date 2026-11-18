"""Self-hosted sniffer and metadata extractor for the .pwm media container.

Container layout (all integers big-endian)::

    header:  magic[4] version[1] header_len[2] track_count[4] declared_len[4]
    table:   track_count * track_entry (fixed 52 bytes, see TRACK_FORMAT)
    payload: interleaved track payload blobs

The parser never trusts declared sizes: every offset is bounds-checked
before use and failures carry the byte offset of the offending field.
"""
from __future__ import annotations

import hashlib
import struct
from pathlib import Path

from .model import (
    KIND_NAMES,
    KNOWN_CODECS,
    MAX_FILE_BYTES,
    MAX_TRACKS,
    SUPPORTED_VERSION,
    Media,
    Track,
    TrackError,
)

MAGIC = b"PWMC"
HEADER_FORMAT = ">4sBHII"
HEADER_SIZE = struct.calcsize(HEADER_FORMAT)  # 15
TRACK_FORMAT = ">IB4sIIIIIIIH16sB"
TRACK_ENTRY_SIZE = struct.calcsize(TRACK_FORMAT)  # 56

# Byte offsets of header fields, used in diagnostics.
OFF_MAGIC = 0
OFF_VERSION = 4
OFF_HEADER_LEN = 5
OFF_TRACK_COUNT = 7
OFF_DECLARED_LEN = 11


class FileAccessError(OSError):
    """Raised when a container path cannot be read at all."""


def _err(code: str, message: str, offset: int | None = None,
         track_id: int | None = None) -> TrackError:
    return TrackError(code=code, message=message, offset=offset, track_id=track_id)


def _decode_language(raw: bytes) -> tuple[str, dict[str, int | str] | None]:
    text = raw.rstrip(b"\x00")
    try:
        return text.decode("ascii"), None
    except UnicodeDecodeError:
        return "und", {"language_raw_hex": raw.hex()}


def parse_container(data: bytes, path: str) -> Media:
    """Parse in-memory container bytes; never raises on malformed content."""
    actual_length = len(data)
    media = Media(
        path=path, version=0, header_length=0, track_count=0,
        declared_length=0, actual_length=actual_length, tracks=[],
    )
    if actual_length > MAX_FILE_BYTES:
        media.errors.append(_err(
            "E_FILE_TOO_LARGE",
            f"file is {actual_length} bytes, limit is {MAX_FILE_BYTES}",
        ))
        return media
    if actual_length < HEADER_SIZE:
        media.errors.append(_err(
            "E_TRUNCATED_HEADER",
            f"need {HEADER_SIZE} header bytes, found {actual_length}",
            offset=0,
        ))
        return media

    magic, version, header_len, track_count, declared_len = struct.unpack_from(
        HEADER_FORMAT, data, 0
    )
    media.version = version
    media.header_length = header_len
    media.track_count = track_count
    media.declared_length = declared_len

    if magic != MAGIC:
        media.errors.append(_err(
            "E_MAGIC", f"bad magic {magic!r}, expected {MAGIC!r}", OFF_MAGIC
        ))
        return media
    if version != SUPPORTED_VERSION:
        media.errors.append(_err(
            "E_VERSION", f"unsupported container version {version}", OFF_VERSION
        ))
        return media
    if header_len != HEADER_SIZE:
        media.errors.append(_err(
            "E_HEADER_LENGTH",
            f"header_len={header_len} but version {version} requires "
            f"{HEADER_SIZE}",
            OFF_HEADER_LEN,
        ))
        return media
    if track_count > MAX_TRACKS:
        media.errors.append(_err(
            "E_TRACK_COUNT_LIMIT",
            f"track_count={track_count} exceeds limit {MAX_TRACKS}",
            OFF_TRACK_COUNT,
        ))
        return media
    if declared_len != actual_length:
        media.errors.append(_err(
            "E_DECLARED_LENGTH",
            f"declared length {declared_len} != actual length {actual_length}",
            OFF_DECLARED_LEN,
        ))

    table_end = header_len + track_count * TRACK_ENTRY_SIZE
    if table_end > actual_length:
        media.errors.append(_err(
            "E_TRUNCATED_TABLE",
            f"track table ends at byte {table_end}, file has {actual_length}",
            offset=header_len,
        ))
        return media

    seen_ids: set[int] = set()
    payload_cursor = table_end
    for index in range(track_count):
        entry_off = header_len + index * TRACK_ENTRY_SIZE
        (track_id, kind, codec_raw, timebase, start_ts, sample_count,
         width, height, sample_rate, channels, lang_len, lang_raw,
         flags) = struct.unpack_from(TRACK_FORMAT, data, entry_off)
        codec_id = codec_raw.decode("latin-1")
        language, lang_raw_field = _decode_language(lang_raw)
        if lang_len > len(lang_raw):
            media.errors.append(_err(
                "E_LANG_LENGTH",
                f"track {track_id} language length {lang_len} exceeds the "
                f"16-byte label field",
                offset=entry_off + 39, track_id=track_id,
            ))
        payload_offset = payload_cursor
        payload_length = sample_count  # payload byte count equals sample count
        raw: dict[str, int | str] = {}
        unrecognized = False
        if kind not in KIND_NAMES:
            unrecognized = True
            raw["kind"] = kind
        if codec_id not in KNOWN_CODECS:
            unrecognized = True
            raw["codec_id"] = codec_id
        if lang_raw_field:
            unrecognized = True
            raw.update(lang_raw_field)
        if timebase == 0:
            media.errors.append(_err(
                "E_ZERO_TIMEBASE",
                f"track {track_id} declares timebase 0",
                offset=entry_off + 9, track_id=track_id,
            ))
        if payload_offset + payload_length > declared_len:
            media.errors.append(_err(
                "E_ENTRY_OVERRUN",
                f"track {track_id} payload [{payload_offset},"
                f"{payload_offset + payload_length}) exceeds declared "
                f"length {declared_len}",
                offset=entry_off, track_id=track_id,
            ))
        if track_id in seen_ids:
            media.errors.append(_err(
                "E_DUPLICATE_TRACK_ID",
                f"duplicate track id {track_id}",
                offset=entry_off, track_id=track_id,
            ))
        seen_ids.add(track_id)
        media.tracks.append(Track(
            track_id=track_id,
            kind=kind,
            kind_name=KIND_NAMES.get(kind, "unknown"),
            codec_id=codec_id,
            timebase=timebase,
            start_ts=start_ts,
            sample_count=sample_count,
            width=width,
            height=height,
            sample_rate=sample_rate,
            channels=channels,
            language=language,
            payload_offset=payload_offset,
            payload_length=payload_length,
            flags=flags,
            unrecognized=unrecognized,
            raw_fields=raw,
        ))
        payload_cursor += payload_length
    return media


def probe_file(path: str | Path) -> Media:
    """Read and sniff one container file, attaching its content hash."""
    p = Path(path)
    try:
        data = p.read_bytes()
    except OSError as exc:  # missing / unreadable path
        raise FileAccessError(f"cannot read {p}: {exc}") from exc
    media = parse_container(data, str(p))
    media.content_hash = hashlib.sha256(data).hexdigest()
    return media
