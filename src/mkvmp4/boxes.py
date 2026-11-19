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

BOX_HEADER_PLACEHOLDER = 8


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
        self._f.write(b"\x00" * BOX_HEADER_PLACEHOLDER + box_type)
        self._stack.append(_OpenBox(box_type, pos, pos + 8))

    def end_box(self) -> int:
        if not self._stack:
            raise ValueError("end_box without a matching open_box")
        opened = self._stack.pop()
        end = self.tell()
        payload_size = end - opened.payload_start
        self._f.seek(opened.header_pos)
        self._f.write(box_header(opened.box_type, payload_size))
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
