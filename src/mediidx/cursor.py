"""A bounded big-endian reader over a box payload."""

from __future__ import annotations

import struct

from .errors import StructureError

_U64 = struct.Struct(">Q")
_U32 = struct.Struct(">I")
_U16 = struct.Struct(">H")
_U8 = struct.Struct(">B")


class Cursor:
    """A bounded big-endian reader over a box payload."""

    __slots__ = ("view", "pos", "end", "box_type", "box_offset")

    def __init__(
        self, view: memoryview, start: int, end: int, box_type: bytes, box_offset: int
    ) -> None:
        self.view = view
        self.pos = start
        self.end = end
        self.box_type = box_type
        self.box_offset = box_offset

    @property
    def remaining(self) -> int:
        return self.end - self.pos

    def _need(self, count: int) -> None:
        if count < 0 or self.pos + count > self.end:
            raise StructureError(
                f"payload is truncated: need {count} bytes at payload offset "
                f"{self.pos - self.box_offset}, only {self.remaining} remain",
                box_type=self.box_type,
                offset=self.box_offset,
            )

    def u8(self) -> int:
        self._need(1)
        value = _U8.unpack_from(self.view, self.pos)[0]
        self.pos += 1
        return value

    def u16(self) -> int:
        self._need(2)
        value = _U16.unpack_from(self.view, self.pos)[0]
        self.pos += 2
        return value

    def u32(self) -> int:
        self._need(4)
        value = _U32.unpack_from(self.view, self.pos)[0]
        self.pos += 4
        return value

    def u64(self) -> int:
        self._need(8)
        value = _U64.unpack_from(self.view, self.pos)[0]
        self.pos += 8
        return value

    def take(self, count: int) -> memoryview:
        self._need(count)
        chunk = self.view[self.pos : self.pos + count]
        self.pos += count
        return chunk

    def skip(self, count: int) -> None:
        self._need(count)
        self.pos += count

    def require_done(self, what: str) -> None:
        if self.remaining:
            raise StructureError(
                f"{what} has {self.remaining} unexpected trailing bytes",
                box_type=self.box_type,
                offset=self.box_offset,
            )
