"""Streaming EBML reader with unknown-length boundary detection.

The reader never buffers a whole file: elements are discovered one header at
a time and payloads are read on demand. Master elements with an unknown size
are bounded structurally instead of by a length field:

* an unknown-size :class:`Cluster` ends at the next Segment-level sibling
  (another Cluster, Info, Tracks, SeekHead or Cues) or at a clean EOF;
* an unknown-size :class:`Segment` ends at a clean EOF.

Iteration is driven by an explicit stack of :class:`_Frame` objects rather
than nested generators, so each nesting level keeps its own boundary and
resume position even when the caller reads payloads between iterations.

A truncated header or payload, or a VINT that violates the EBML rules, raises
:class:`~mkvmp4.errors.CorruptMKVError` carrying the byte offset and the
active element path.
"""

import struct
from dataclasses import dataclass

from ..errors import CorruptMKVError, IOFailure, LimitExceededError
from . import schema
from .vint import decode_vint, vint_width

#: Safety limits (nesting / packet size must fail safely).
DEFAULT_MAX_DEPTH = 16
DEFAULT_MAX_ELEMENT_SIZE = 256 * 1024 * 1024  # 256 MiB single payload
DEFAULT_MAX_ELEMENTS = 1_000_000


@dataclass(slots=True)
class Element:
    """One EBML element header plus its location in the file."""

    element_id: int
    offset: int           # absolute byte offset of the ID VINT
    data_offset: int      # absolute byte offset where the payload starts
    size: int             # payload length, or -1 for unknown length
    header_size: int      # ID VINT width + size VINT width
    depth: int

    @property
    def name(self) -> str:
        return schema.name_for(self.element_id)

    @property
    def unknown_size(self) -> bool:
        return self.size < 0

    @property
    def end(self) -> int | None:
        return None if self.size < 0 else self.data_offset + self.size


@dataclass(slots=True)
class _Frame:
    """Per-level iteration state on the parse stack."""

    parent_id: int | None
    parent_name: str
    depth: int
    end: int | None                  # fixed boundary, or None if unknown size
    boundary_ids: frozenset[int]     # sibling IDs that close an unknown master
    resume: int                      # where the next header starts
    yielded_any: bool = False


class EbmlReader:
    """Binary file handle plus a parse stack and bounded iteration."""

    def __init__(self, path: str, *, max_depth: int = DEFAULT_MAX_DEPTH,
                 max_element_size: int = DEFAULT_MAX_ELEMENT_SIZE,
                 max_elements: int = DEFAULT_MAX_ELEMENTS):
        try:
            self._file = open(path, "rb")
        except OSError as exc:
            raise IOFailure(f"cannot open input {path!r}: {exc}") from exc
        self.path = path
        self._file.seek(0, 2)
        self.file_size = self._file.tell()
        self._file.seek(0)
        self.max_depth = max_depth
        self.max_element_size = max_element_size
        self.max_elements = max_elements
        self._elements_seen = 0
        self._stack: list[_Frame] = []

    def __enter__(self) -> "EbmlReader":
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()

    def close(self) -> None:
        try:
            self._file.close()
        except OSError:
            pass

    @property
    def position(self) -> int:
        return self._file.tell()

    def seek(self, offset: int) -> None:
        self._file.seek(offset)

    def element_path(self) -> list[str]:
        return [frame.parent_name for frame in self._stack
                if frame.parent_name]

    # ------------------------------------------------------------------
    # Header decoding
    # ------------------------------------------------------------------
    def _read_exact(self, count: int, where: int) -> bytes:
        try:
            data = self._file.read(count)
        except OSError as exc:
            raise IOFailure(f"read failed at byte {where}: {exc}") from exc
        if len(data) < count:
            raise CorruptMKVError(
                f"unexpected end of file: needed {count} byte(s) at {where}, "
                f"got {len(data)}", offset=where, path=self.element_path())
        return data

    def _read_header_at(self, offset: int, depth: int) -> Element:
        if self._elements_seen >= self.max_elements:
            raise LimitExceededError(
                f"element count limit ({self.max_elements}) exceeded",
                offset=offset, path=self.element_path())
        self._file.seek(offset)
        first = self._read_exact(1, offset)
        id_width = vint_width(first[0], offset)
        id_octets = first + self._read_exact(id_width - 1, offset + 1)
        element_id = 0
        for octet in id_octets:
            element_id = (element_id << 8) | octet
        size_offset = offset + id_width
        size_first = self._read_exact(1, size_offset)
        size_width = vint_width(size_first[0], size_offset)
        size_octets = size_first + self._read_exact(size_width - 1,
                                                    size_offset + 1)
        size, _, _ = decode_vint(size_octets, 0)
        data_offset = size_offset + size_width
        if size >= 0 and size > self.max_element_size:
            raise LimitExceededError(
                f"element {schema.name_for(element_id)} payload {size} bytes "
                f"exceeds limit {self.max_element_size}",
                offset=offset, path=self.element_path())
        self._elements_seen += 1
        return Element(element_id, offset, data_offset, size,
                       id_width + size_width, depth)

    # ------------------------------------------------------------------
    # Stack-driven iteration
    # ------------------------------------------------------------------
    def open_root(self) -> None:
        """Start iteration at the file root (EBML header, Segment)."""
        self._stack = [_Frame(None, "", 0, None, frozenset(), 0)]

    def open_master(self, element: Element) -> None:
        """Push ``element`` onto the parse stack to iterate its children."""
        depth = element.depth + 1
        if depth > self.max_depth:
            raise LimitExceededError(
                f"nesting depth limit ({self.max_depth}) exceeded",
                offset=element.offset, path=self.element_path())
        if element.unknown_size:
            boundary = (schema.SEGMENT_CHILD_IDS
                        if element.element_id == schema.CLUSTER
                        else frozenset())
            end = None
        else:
            boundary = frozenset()
            end = element.end
        self._stack.append(_Frame(
            element.element_id, element.name, depth, end, boundary,
            element.data_offset))

    def close_master(self) -> None:
        """Pop the current frame, restoring the parent's resume position."""
        frame = self._stack.pop()
        if self._stack:
            self._stack[-1].resume = frame.resume

    def next_element(self) -> Element | None:
        """Return the next sibling in the current frame, or ``None`` at end."""
        frame = self._stack[-1]
        pos = frame.resume
        if frame.end is not None:
            if pos > frame.end:
                raise CorruptMKVError(
                    "child payload runs past parent boundary",
                    offset=pos, path=self.element_path())
            if pos == frame.end:
                return None
        else:
            self._file.seek(pos)
            if not self._file.read(1):
                if frame.yielded_any:
                    return None
                raise CorruptMKVError(
                    "unexpected end of file inside unknown-size "
                    f"{frame.parent_name or 'root'}",
                    offset=pos, path=self.element_path())
        element = self._read_header_at(pos, frame.depth)
        if frame.boundary_ids and element.element_id in frame.boundary_ids:
            # The boundary sibling belongs to the parent level; leave the
            # cursor (and resume offset) pointing at its header.
            frame.resume = pos
            return None
        schema.check_parent(element.element_id, frame.parent_id)
        frame.yielded_any = True
        frame.resume = (element.end if not element.unknown_size
                        else element.data_offset)
        self._file.seek(element.data_offset)
        return element

    # ------------------------------------------------------------------
    # Payload access
    # ------------------------------------------------------------------
    def read_payload(self, element: Element) -> bytes:
        """Read the complete (bounded) payload of a leaf element."""
        if element.unknown_size:
            raise CorruptMKVError(
                "cannot read raw payload of an unknown-size master",
                offset=element.offset, path=self.element_path())
        return self.read_range(element.data_offset, element.size)

    def read_range(self, offset: int, count: int) -> bytes:
        """Read an arbitrary absolute byte range (block payloads)."""
        if count > self.max_element_size:
            raise LimitExceededError(
                f"range of {count} bytes exceeds packet limit",
                offset=offset, path=self.element_path())
        self._file.seek(offset)
        return self._read_exact(count, offset)


# ---------------------------------------------------------------------------
# Typed value decoders for leaf elements (big-endian EBML conventions).
# ---------------------------------------------------------------------------
def decode_uint(data: bytes) -> int:
    return int.from_bytes(data, byteorder="big", signed=False)


def decode_sint(data: bytes) -> int:
    return int.from_bytes(data, byteorder="big", signed=True)


def decode_float(data: bytes) -> float:
    if len(data) == 0:
        return 0.0
    if len(data) == 4:
        return struct.unpack(">f", data)[0]
    if len(data) == 8:
        return struct.unpack(">d", data)[0]
    raise CorruptMKVError(
        f"float element must be 0, 4 or 8 bytes, got {len(data)}")


def decode_string(data: bytes) -> str:
    return data.rstrip(b"\x00").decode("utf-8", errors="replace")


def decode_leaf(element: Element, reader: "EbmlReader"):
    """Decode a known leaf element according to its schema type."""
    spec = schema.spec_for(element.element_id)
    if spec is None:
        return reader.read_payload(element)
    data = reader.read_payload(element)
    if spec.elem_type is schema.ElemType.UINT:
        return decode_uint(data)
    if spec.elem_type is schema.ElemType.SINT:
        return decode_sint(data)
    if spec.elem_type is schema.ElemType.FLOAT:
        return decode_float(data)
    if spec.elem_type is schema.ElemType.DATE:
        return decode_sint(data)
    if spec.elem_type in (schema.ElemType.STRING, schema.ElemType.UTF8):
        return decode_string(data)
    return data
