"""ISO BMFF (MP4) box encoding primitives.

Boxes are written big-endian. A :class:`BoxWriter` supports streaming output
with *length backfilling*: container boxes (``moov``, ``trak``, ``mdat`` ...)
are opened with a placeholder header and their real size is patched in when
the box closes, switching to a 64-bit ``largesize`` header automatically when
payloads exceed the 32-bit range.
"""

from __future__ import annotations

import struct
from dataclasses import dataclass


def box_header(box_type: bytes, payload_size: int) -> bytes:
    if len(box_type) != 4:
        raise ValueError("box type must be exactly four bytes")
    total = payload_size + 8
    if total <= 0xFFFFFFFF:
        return struct.pack(">I4s", total, box_type)
    if payload_size + 16 <= 0xFFFFFFFFFFFFFFFF:
        return struct.pack(">I4sQ", 1, box_type, payload_size + 16)
    raise OverflowError("box larger than 64-bit size field")


def fullbox_header(box_type: bytes, version: int, flags: int,
                   payload_size: int) -> bytes:
    return box_header(box_type, payload_size + 4) + struct.pack(
        ">I", (version << 24) | (flags & 0xFFFFFF))


def make_box(box_type: bytes, payload: bytes) -> bytes:
    return box_header(box_type, len(payload)) + payload


def make_fullbox(box_type: bytes, version: int, flags: int,
                 payload: bytes) -> bytes:
    return fullbox_header(box_type, version, flags, len(payload)) + payload


@dataclass(slots=True)
class _OpenBox:
    box_type: bytes
    header_pos: int
    payload_start: int


class BoxWriter:
    """Seekable-stream writer with deferred container-length backfill."""

    def __init__(self, stream):
        self._f = stream
        self._stack: list[_OpenBox] = []

    def tell(self) -> int:
        return self._f.tell()

    def write_raw(self, data: bytes | bytearray) -> None:
        self._f.write(bytes(data))

    def write_box(self, box_type: bytes, payload: bytes) -> int:
        start = self.tell()
        self.write_raw(make_box(box_type, payload))
        return self.tell() - start

    def open_box(self, box_type: bytes) -> None:
        pos = self.tell()
        # Standard 8-byte header placeholder; type is written immediately so
        # end_box only has to backfill the leading 4-byte size.
        self._f.write(b"\x00" * 4 + box_type)
        self._stack.append(_OpenBox(box_type, pos, pos + 8))

    def end_box(self) -> int:
        if not self._stack:
            raise ValueError("end_box without a matching open_box")
        opened = self._stack.pop()
        end = self.tell()
        payload_size = end - opened.payload_start
        header = box_header(opened.box_type, payload_size)
        if len(header) != 8:
            raise OverflowError(
                "streamed containers must fit a 32-bit box size; got a box "
                f"requiring largesize at offset {opened.header_pos}")
        self._f.seek(opened.header_pos)
        self._f.write(header)  # exactly 8 bytes: size + type, in place
        self._f.seek(end)
        return end - opened.header_pos

    def close(self) -> None:
        if self._stack:
            raise ValueError(
                f"unclosed boxes: {[b.box_type for b in self._stack]}")


# --------------------------------------------------------------------------
# Fixed-point and small field helpers
# --------------------------------------------------------------------------

def fixed16(value: float) -> bytes:
    return struct.pack(">H", int(round(value * 65536.0)) & 0xFFFF)


def fixed32(value: float) -> bytes:
    return struct.pack(">I", int(round(value * 65536.0)) & 0xFFFFFFFF)


def pascal_string(text: str, padded_length: int = 32) -> bytes:
    raw = text.encode("utf-8")
    room = padded_length - 1
    if len(raw) > room:
        raw = raw[:room]
    out = bytes([len(raw)]) + raw
    return out + b"\x00" * (padded_length - len(out))


# --------------------------------------------------------------------------
# File-level and structural boxes
# --------------------------------------------------------------------------

def ftyp_box() -> bytes:
    payload = b"isom" + struct.pack(">I", 0x200) + b"isomiso2avc1mp41"
    return make_box(b"ftyp", payload)


def mvhd_box(timescale: int, duration_units: int, next_track_id: int) -> bytes:
    # Version 1: 64-bit creation/modification/duration fields.
    payload = b"".join([
        struct.pack(">QQ", 0, 0),                 # creation/modification time
        struct.pack(">I", timescale),
        struct.pack(">Q", duration_units),
        struct.pack(">I", 0x00010000),            # 1.0 preferred rate
        struct.pack(">H", 0x0100),                # 1.0 volume
        b"\x00" * 10,                             # reserved
        struct.pack(">9I",                        # identity matrix
                    0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000),
        b"\x00" * 24,                             # pre-defined
        struct.pack(">I", next_track_id),
    ])
    return make_fullbox(b"mvhd", 1, 0, payload)


def tkhd_box(track_id: int, duration_units: int,
             width: int, height: int, is_audio: bool) -> bytes:
    flags = 0x7  # track_enabled | track_in_movie | track_in_preview
    payload = b"".join([
        struct.pack(">QQ", 0, 0),
        struct.pack(">I", track_id),
        struct.pack(">I", 0),                     # reserved
        struct.pack(">Q", duration_units),
        b"\x00" * 8,                              # reserved
        struct.pack(">HH", 0, 0),                 # layer, alternate group
        struct.pack(">H", 0x0100 if not is_audio else 0),
        struct.pack(">HH", 0, 0),                 # reserved
        struct.pack(">9I",
                    0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000),
        fixed16(float(width)),
        fixed16(float(height)),
    ])
    return make_fullbox(b"tkhd", 1, flags, payload)


def mdhd_box(timescale: int, duration_units: int) -> bytes:
    payload = b"".join([
        struct.pack(">QQ", 0, 0),
        struct.pack(">I", timescale),
        struct.pack(">Q", duration_units),
        struct.pack(">HH", 0x55C4, 0),            # 'und' language, quality
    ])
    return make_fullbox(b"mdhd", 1, 0, payload)


def hdlr_box(handler_type: bytes, name: str) -> bytes:
    if handler_type not in (b"vide", b"soun"):
        raise ValueError(f"unsupported handler type {handler_type!r}")
    payload = (
        struct.pack(">I", 0)                      # pre-defined
        + handler_type
        + b"\x00" * 12                            # reserved
        + name.encode("utf-8") + b"\x00"
    )
    return make_fullbox(b"hdlr", 0, 0, payload)


def vmhd_box() -> bytes:
    return make_fullbox(b"vmhd", 0, 1, struct.pack(">HHHH", 0, 0, 0, 0))


def smhd_box() -> bytes:
    return make_fullbox(b"smhd", 0, 0, struct.pack(">HH", 0, 0))


def nmhd_box() -> bytes:
    return make_fullbox(b"nmhd", 0, 0, b"")


def dinf_dref_box() -> bytes:
    url = make_fullbox(b"url ", 0, 1, b"")
    dref = make_fullbox(b"dref", 0, 0, struct.pack(">I", 1) + url)
    return make_box(b"dinf", dref)


def elst_box(media_start_units: int, duration_units: int,
             timescale: int) -> bytes:
    """One-entry edit list mapping movie time onto the media timeline.

    A non-zero ``media_start_units`` shifts a track whose first sample does
    not sit at media time zero (legal in Matroska after the global shift).
    Version 1 uses 64-bit media time fields.
    """
    payload = b"".join([
        struct.pack(">I", 1),                              # entry count
        struct.pack(">Q", duration_units),                 # segment duration
        struct.pack(">q", media_start_units),              # media time
        struct.pack(">I", 0x00010000),                     # rate 1.0 fixed16.16
    ])
    return make_box(b"edts", make_fullbox(b"elst", 1, 0, payload))


# --------------------------------------------------------------------------
# Sample entry boxes (avc1 / mp4a) and codec configuration
# --------------------------------------------------------------------------

def _descriptor(tag: int, payload: bytes) -> bytes:
    """Wrap an MPEG-4 object descriptor with expandable length encoding."""
    size = len(payload)
    header = bytearray([tag])
    while True:
        byte = size & 0x7F
        size >>= 7
        if size:
            header.append(byte | 0x80)
        else:
            header.append(byte)
            break
    return bytes(header) + payload


def esds_box(audio_specific_config: bytes) -> bytes:
    dsi = _descriptor(0x05, audio_specific_config)
    dec_config = _descriptor(0x04, b"".join([
        bytes([0x40, 0x15]),                    # AAC object type, audio stream
        b"\x00\x00\x00",                        # buffer size DB
        struct.pack(">II", 0, 0),               # max / average bitrate
        dsi,
    ]))
    sl_config = _descriptor(0x06, b"\x02")
    es_desc = _descriptor(0x03, struct.pack(">H", 0) + b"\x00" + dec_config
                          + sl_config)
    return make_fullbox(b"esds", 0, 0, es_desc)


def avc1_box(width: int, height: int, avc_config: bytes) -> bytes:
    avcc = make_box(b"avcC", avc_config)
    payload = b"".join([
        b"\x00" * 6,
        struct.pack(">H", 1),                   # data reference index
        b"\x00" * 2 + b"\x00" * 12,             # pre-defined + reserved
        struct.pack(">HH", width, height),
        struct.pack(">II", 0x00480000, 0x00480000),  # 72 dpi
        struct.pack(">I", 0),                   # reserved
        struct.pack(">H", 1),                   # frame count
        pascal_string(""),                      # compressor name
        struct.pack(">Hh", 0x0018, -1),         # depth, pre-defined
        avcc,
    ])
    return make_box(b"avc1", payload)


def mp4a_box(channels: int, sample_size: int, sample_rate: float,
             audio_specific_config: bytes) -> bytes:
    payload = b"".join([
        b"\x00" * 6,
        struct.pack(">H", 1),                   # data reference index
        b"\x00" * 8,                            # reserved
        struct.pack(">HH", channels, sample_size),
        struct.pack(">HH", 0, 0),               # pre-defined, reserved
        struct.pack(">I", int(round(sample_rate)) << 16),
        esds_box(audio_specific_config),
    ])
    return make_box(b"mp4a", payload)


def stsd_box(sample_entry: bytes) -> bytes:
    return make_fullbox(b"stsd", 0, 0, struct.pack(">I", 1) + sample_entry)


# --------------------------------------------------------------------------
# Sample table boxes
# --------------------------------------------------------------------------

def stts_box(durations: list[int]) -> bytes:
    runs: list[tuple[int, int]] = []
    for delta in durations:
        if runs and runs[-1][1] == delta:
            runs[-1] = (runs[-1][0] + 1, delta)
        else:
            runs.append((1, delta))
    payload = struct.pack(">I", len(runs))
    payload += b"".join(struct.pack(">II", count, delta)
                        for count, delta in runs)
    return make_fullbox(b"stts", 0, 0, payload)


def stss_box(keyframe_numbers: list[int]) -> bytes:
    payload = struct.pack(">I", len(keyframe_numbers))
    payload += b"".join(struct.pack(">I", n) for n in keyframe_numbers)
    return make_fullbox(b"stss", 0, 0, payload)


def stsc_box(samples_per_chunk: int) -> bytes:
    # One run starting at the first chunk: every chunk carries the same
    # number of samples and uses sample description index 1.
    payload = struct.pack(">I", 1) + struct.pack(
        ">III", 1, samples_per_chunk, 1)
    return make_fullbox(b"stsc", 0, 0, payload)


def stsz_box(sizes: list[int]) -> bytes:
    payload = struct.pack(">II", 0, len(sizes))
    payload += b"".join(struct.pack(">I", size) for size in sizes)
    return make_fullbox(b"stsz", 0, 0, payload)


def chunk_offset_box(offsets: list[int]) -> bytes:
    """Emit stco when all offsets fit in 32 bits, otherwise co64."""
    if all(0 <= off <= 0xFFFFFFFF for off in offsets):
        payload = struct.pack(">I", len(offsets))
        payload += b"".join(struct.pack(">I", off) for off in offsets)
        return make_fullbox(b"stco", 0, 0, payload)
    payload = struct.pack(">I", len(offsets))
    payload += b"".join(struct.pack(">Q", off) for off in offsets)
    return make_fullbox(b"co64", 0, 0, payload)
