"""Bounded binary primitives shared by the container probes.

Every read in this module is validated against an explicit limit before any
bytes are consumed.  Parsers are expected to keep the ``Reader`` limit equal
to the end of the parent structure so that a corrupt child length can never
escape its parent.
"""

from __future__ import annotations

MAX_VINT_LEN = 8


class BinaryError(Exception):
    """A bounded read failed.

    ``offset`` is the absolute file offset where the failing read started and
    ``reason`` is a stable, human readable explanation suitable for JSON.
    """

    def __init__(self, reason: str, offset: int):
        self.reason = reason
        self.offset = offset
        super().__init__(f"{reason} at offset {offset}")


class Reader:
    """A cursor over ``data`` that cannot read outside ``[start, limit)``."""

    __slots__ = ("data", "start", "limit", "pos", "name")

    def __init__(self, data: bytes, start: int = 0, limit: int | None = None,
                 name: str = "input"):
        if limit is None:
            limit = len(data)
        if start < 0 or limit < start or limit > len(data):
            raise BinaryError(
                f"invalid reader bounds start={start} limit={limit} "
                f"size={len(data)}", start if start >= 0 else 0)
        self.data = data
        self.start = start
        self.limit = limit
        self.pos = start
        self.name = name

    # -- state ---------------------------------------------------------
    @property
    def remaining(self) -> int:
        return self.limit - self.pos

    @property
    def exhausted(self) -> bool:
        return self.pos >= self.limit

    def require(self, count: int, what: str = "read") -> None:
        if count < 0:
            raise BinaryError(f"negative {what} length {count}", self.pos)
        if count > self.limit - self.pos:
            raise BinaryError(
                f"{what} of {count} byte(s) exceeds limit "
                f"({self.limit - self.pos} remaining)", self.pos)

    # -- byte level ----------------------------------------------------
    def read_bytes(self, count: int) -> bytes:
        self.require(count)
        chunk = self.data[self.pos:self.pos + count]
        self.pos += count
        return chunk

    def skip(self, count: int, what: str = "skip") -> None:
        self.require(count, what)
        self.pos += count

    def read_uint(self, size: int) -> int:
        """Read ``size`` big-endian bytes as an unsigned integer."""
        self.require(size, "integer")
        value = int.from_bytes(self.data[self.pos:self.pos + size], "big")
        self.pos += size
        return value

    def read_uint8(self) -> int:
        return self.read_uint(1)

    def read_uint16(self) -> int:
        return self.read_uint(2)

    def read_uint32(self) -> int:
        return self.read_uint(4)

    def read_uint64(self) -> int:
        return self.read_uint(8)

    def read_int16(self) -> int:
        self.require(2, "integer")
        value = int.from_bytes(self.data[self.pos:self.pos + 2], "big",
                               signed=True)
        self.pos += 2
        return value

    def subreader(self, count: int, name: str | None = None) -> "Reader":
        """Return a reader covering the next ``count`` bytes and advance."""
        self.require(count, "subreader")
        child = Reader(self.data, self.pos, self.pos + count,
                       name or f"{self.name}[{self.pos}:{self.pos + count}]")
        self.pos += count
        return child

    def __repr__(self) -> str:  # pragma: no cover - debugging aid
        return f"<Reader {self.name} pos={self.pos} limit={self.limit}>"


def checked_box_end(offset: int, size: int, limit: int,
                    what: str = "box") -> int:
    """Validate ``offset + size`` against ``limit`` and return the end offset."""
    if size < 0:
        raise BinaryError(f"negative {what} size {size}", offset)
    end = offset + size  # Python ints do not overflow; the limit check matters
    if end > limit:
        raise BinaryError(
            f"{what} end {end} exceeds limit {limit} (declared size {size})",
            offset)
    return end


def ticks_to_ms(ticks: int, timescale: int) -> float:
    """Convert integer ticks to milliseconds without float overflow."""
    if timescale <= 0:
        raise ValueError(f"timescale must be positive, got {timescale}")
    return ticks * 1000.0 / timescale
