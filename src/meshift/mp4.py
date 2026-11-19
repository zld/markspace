"""MP4 (ISO Base Media) writer for remuxed, never re-encoded samples."""

from __future__ import annotations

import struct
from dataclasses import dataclass

from . import MAX_OUTPUT_BYTES
from .errors import MP4WriteError, SizeLimitError, UnsupportedCodecError
from .matroska import TRACK_AUDIO, TRACK_VIDEO
from .timeline import NormalizedTrack, OUTPUT_TIMESCALE

# Codec identifiers that meshift can place inside an MP4 container without
# decoding. Anything outside this mapping is rejected explicitly.
CODEC_MAPPING = {
    "V_VP9": ("vp09", "video", TRACK_VIDEO),
    "V_VP8": ("vp08", "video", TRACK_VIDEO),
    "V_AV1": ("av01", "video", TRACK_VIDEO),
    "V_MPEG4/ISO/AVC": ("avc1", "video", TRACK_VIDEO),
    "A_AAC": ("mp4a", "audio", TRACK_AUDIO),
    "A_AC3": ("ac-3", "audio", TRACK_AUDIO),
    "A_OPUS": ("Opus", "audio", TRACK_AUDIO),
}

FTYP_BRANDS = b"isomiso5mp42"


@dataclass
class MP4Layout:
    payload: bytes
    mdat_start: int
    mdat_end: int
    chunk_offsets: list[list[int]]


def box(box_type: bytes, payload: bytes = b"") -> bytes:
    if len(box_type) != 4:
        raise MP4WriteError("box type must be exactly four bytes")
    return struct.pack(">I", len(payload) + 8) + box_type + payload


def fullbox(box_type: bytes, version: int, flags: int, payload: bytes) -> bytes:
    head = bytes([version]) + struct.pack(">I", flags)[1:]
    return box(box_type, head + payload)


def check_codec(codec_id: str, kind: int) -> str:
    if codec_id not in CODEC_MAPPING:
        raise UnsupportedCodecError(
            f"codec {codec_id!r} is not supported for MP4 remux")
    fourcc, _, expected_kind = CODEC_MAPPING[codec_id]
    if expected_kind != kind:
        raise UnsupportedCodecError(
            f"codec {codec_id!r} does not match track kind {kind}")
    return fourcc


def _sample_entry(track: NormalizedTrack) -> bytes:
    fourcc = check_codec(track.codec_id, track.kind)
    if track.kind == TRACK_VIDEO:
        payload = (b"\x00" * 6 + struct.pack(">H", 1)
                   + b"\x00" * 16 + struct.pack(">HH", 320, 240)
                   + struct.pack(">II", 0x00480000, 0x00480000)
                   + struct.pack(">I", 0) + struct.pack(">H", 1)
                   + b"\x00" * 32)
    else:
        payload = (b"\x00" * 6 + struct.pack(">H", 1) + b"\x00" * 8
                   + struct.pack(">HH", 2, 16) + b"\x00" * 8
                   + struct.pack(">I", 48000 << 16) + struct.pack(">H", 0)
                   + b"\x00" * 8)
    return box(b"stsd", fullbox(
        b"stsd", 0, 0, struct.pack(">I", 1)
        + box(fourcc.encode("ascii"), payload)))


def _stts(track: NormalizedTrack) -> bytes:
    entries: list[tuple[int, int]] = []
    for sample in track.samples:
        if entries and entries[-1][1] == sample.duration_ns:
            entries[-1] = (entries[-1][0] + 1, sample.duration_ns)
        else:
            entries.append((1, sample.duration_ns))
    payload = struct.pack(">I", len(entries))
    for count, duration in entries:
        payload += struct.pack(">II", count, duration)
    return fullbox(b"stts", 0, 0, payload)


def _stsz(track: NormalizedTrack) -> bytes:
    payload = struct.pack(">II", 0, len(track.samples))
    payload += b"".join(struct.pack(">I", len(s.payload))
                        for s in track.samples)
    return fullbox(b"stsz", 0, 0, payload)


def _stsc() -> bytes:
    return fullbox(b"stsc", 0, 0,
                   struct.pack(">I", 1) + struct.pack(">III", 1, 1, 1))


def _co64(offsets: list[int]) -> bytes:
    payload = struct.pack(">I", len(offsets))
    payload += b"".join(struct.pack(">Q", offset) for offset in offsets)
    return fullbox(b"co64", 0, 0, payload)


def _stss(track: NormalizedTrack) -> bytes:
    keyframes = [idx + 1 for idx, s in enumerate(track.samples)
                 if s.is_keyframe]
    payload = struct.pack(">I", len(keyframes))
    payload += b"".join(struct.pack(">I", idx) for idx in keyframes)
    return fullbox(b"stss", 0, 0, payload)


def _ctts(track: NormalizedTrack) -> bytes:
    entries: list[tuple[int, int]] = []
    for sample in track.samples:
        if entries and entries[-1][1] == sample.composition_offset_ns:
            entries[-1] = (entries[-1][0] + 1,
                           sample.composition_offset_ns)
        else:
            entries.append((1, sample.composition_offset_ns))
    payload = struct.pack(">I", len(entries))
    for count, offset in entries:
        # Version 1 ctts treats sample_offset as signed; the wire field is
        # still a 32-bit word, so encode through the signed integer range.
        payload += struct.pack(">II", count, offset & 0xFFFFFFFF)
    return fullbox(b"ctts", 1, 0, payload)


def _minf(track: NormalizedTrack, offsets: list[int]) -> bytes:
    media_header = fullbox(b"vmhd" if track.kind == TRACK_VIDEO
                           else b"smhd", 0, 0, b"\x00" * 8)
    dinf = box(b"dinf", box(b"dref", fullbox(
        b"dref", 0, 0, struct.pack(">I", 1)
        + box(b"url ", fullbox(b"url ", 0, 1, b"")))))
    stbl = box(b"stbl", _sample_entry(track) + _stts(track) + _ctts(track)
               + _stsz(track) + _stsc() + _co64(offsets)
               + (_stss(track) if track.kind == TRACK_VIDEO else b""))
    return box(b"minf", media_header + dinf + stbl)


def _mdia(track: NormalizedTrack, offsets: list[int]) -> bytes:
    mdhd = fullbox(b"mdhd", 1, 0,
                   struct.pack(">QQIQ", 0, 0, OUTPUT_TIMESCALE,
                               track.last_dts_ns)
                   + struct.pack(">HH", 0x55C4, 0))
    handler = (b"\x00" * 4
               + (b"vide" if track.kind == TRACK_VIDEO else b"soun")
               + b"\x00" * 12 + b"meshift\x00")
    return box(b"mdia", mdhd
               + box(b"hdlr", fullbox(b"hdlr", 0, 0, handler))
               + _minf(track, offsets))


def _trak(track: NormalizedTrack, offsets: list[int]) -> bytes:
    # version 1 tkhd: creation, modification, track id, reserved, duration,
    # reserved(8), layer/altgroup/volume/reserved, then the 3x3 matrix.
    matrix = struct.pack(">9I", 0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0,
                         0x40000000)
    tkhd_payload = (struct.pack(">QQIIQ", 0, 0, track.output_number, 0,
                                track.last_dts_ns)
                    + b"\x00" * 8 + struct.pack(">HHh", 0, 0, 0)
                    + b"\x00" * 2 + matrix + struct.pack(">II", 320 << 16,
                                                         240 << 16))
    flags = 3 if track.kind == TRACK_VIDEO else 1
    tkhd = fullbox(b"tkhd", 1, flags, tkhd_payload)
    return box(b"trak", tkhd + _mdia(track, offsets))


def build_moov(tracks: list[NormalizedTrack],
               chunk_offsets: list[list[int]]) -> bytes:
    duration = max((t.last_dts_ns for t in tracks), default=0)
    mvhd = fullbox(b"mvhd", 1, 0,
                   struct.pack(">QQII", 0, OUTPUT_TIMESCALE, duration,
                               0x00010000))
    traks = b"".join(_trak(t, offsets)
                     for t, offsets in zip(tracks, chunk_offsets))
    return box(b"moov", mvhd + traks)


def build_mp4(tracks: list[NormalizedTrack]) -> MP4Layout:
    """Serialize ftyp + moov + mdat and validate the size ceiling."""
    ftyp = box(b"ftyp", b"isom" + struct.pack(">I", 0x200) + FTYP_BRANDS)
    media = b"".join(s.payload for t in tracks for s in t.samples)

    # Probe moov size with placeholder offsets so the mdat base is exact.
    placeholders = [[0] * len(t.samples) for t in tracks]
    probe_moov = build_moov(tracks, placeholders)
    mdat_base = len(ftyp) + len(probe_moov) + 8

    chunk_offsets: list[list[int]] = []
    cursor = mdat_base
    for track in tracks:
        offsets = []
        for sample in track.samples:
            offsets.append(cursor)
            cursor += len(sample.payload)
        chunk_offsets.append(offsets)
    moov = build_moov(tracks, chunk_offsets)
    if len(moov) != len(probe_moov):
        raise MP4WriteError("non-deterministic moov size during layout")

    total = len(ftyp) + len(moov) + 8 + len(media)
    if total > MAX_OUTPUT_BYTES:
        raise SizeLimitError(
            f"output would be {total} bytes, limit is {MAX_OUTPUT_BYTES}")
    return MP4Layout(ftyp + moov + box(b"mdat", media),
                     mdat_base, mdat_base + len(media), chunk_offsets)
