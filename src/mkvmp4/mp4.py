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
