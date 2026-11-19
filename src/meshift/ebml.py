"""Minimal EBML parser used by the Matroska reading layer.

The parser is deliberately generic: it turns a byte buffer into an element
tree without any Matroska-specific decoding beyond human-readable names.
Unknown-length elements are closed using the boundary of their enclosing
parent or the next recognised sibling element.
"""

from __future__ import annotations

from dataclasses import dataclass, field

from .errors import EBMLError, TruncatedInputError

# --- Matroska / EBML element identifiers -----------------------------------
EBML_HEADER = 0x1A45DFA3
SEGMENT = 0x18538067
SEEK_HEAD = 0x114D9B74
VOID = 0xEC
CRC32 = 0xBF
INFO = 0x1549A66A
TIMECODE_SCALE = 0x2AD7B1
DURATION = 0x4489
TRACKS = 0x1654AE6B
TRACK_ENTRY = 0xAE
TRACK_NUMBER = 0xD7
TRACK_UID = 0x73C5
TRACK_TYPE = 0x83
CODEC_ID = 0x86
DEFAULT_DURATION = 0x23E383
CLUSTER = 0x1F43B675
TIMECODE = 0xE7
SIMPLE_BLOCK = 0xA3
BLOCK_GROUP = 0xA0
BLOCK = 0xA1
REFERENCE_BLOCK = 0xFB
BLOCK_DURATION = 0x9B
# Private two-octet EBML id (class B) carrying signed composition offsets for
# plain Block elements. It lives inside BlockGroup alongside Block.
COMPOSITION_OFFSET = 0x55C2

_ELEMENT_NAMES = {
    EBML_HEADER: "EBML",
    SEGMENT: "Segment",
    SEEK_HEAD: "SeekHead",
    VOID: "Void",
    CRC32: "CRC-32",
    INFO: "Info",
    TIMECODE_SCALE: "TimecodeScale",
    DURATION: "Duration",
    TRACKS: "Tracks",
    TRACK_ENTRY: "TrackEntry",
    TRACK_NUMBER: "TrackNumber",
    TRACK_UID: "TrackUID",
    TRACK_TYPE: "TrackType",
    CODEC_ID: "CodecID",
    DEFAULT_DURATION: "DefaultDuration",
    CLUSTER: "Cluster",
    TIMECODE: "Timecode",
    SIMPLE_BLOCK: "SimpleBlock",
    BLOCK_GROUP: "BlockGroup",
    BLOCK: "Block",
    REFERENCE_BLOCK: "ReferenceBlock",
    BLOCK_DURATION: "BlockDuration",
    COMPOSITION_OFFSET: "MeshiftCompositionOffset",
}

#: Elements that may carry child elements.
CONTAINERS = frozenset({
    EBML_HEADER, SEGMENT, SEEK_HEAD, INFO, TRACKS, TRACK_ENTRY,
    CLUSTER, BLOCK_GROUP,
})

#: Elements legal at document/top level. An unknown-length Segment closes
#: when one of these siblings (or the buffer end) is reached.
TOPLEVEL_IDS = frozenset({EBML_HEADER, SEGMENT, SEEK_HEAD, VOID, CRC32})

#: Direct children of a Segment. An unknown-length Cluster (the only nested
#: unknown-length master we emit) closes at the next Cluster/top-level sibling.
SECTION_IDS = frozenset({SEEK_HEAD, INFO, TRACKS, CLUSTER, VOID, CRC32})

#: Boundary siblings used when an unknown-length element needs an explicit
#: end marker instead of a declared byte length.
_BOUNDARIES = {
    SEGMENT: TOPLEVEL_IDS,
    CLUSTER: SECTION_IDS,
}

_MAX_VINT_WIDTH = 8


def element_name(element_id: int) -> str:
    """Return the human-readable name for *element_id*."""
    return _ELEMENT_NAMES.get(element_id, f"Unknown_{element_id:02X}")


def _vint_width(first_octet: int, offset: int) -> int:
    if first_octet == 0:
        raise EBMLError("illegal VINT: zero first octet", offset)
    # VINT width is one plus the count of leading zero bits.
    width = 9 - first_octet.bit_length()
    if width > _MAX_VINT_WIDTH:
        raise EBMLError(f"illegal VINT width {width}", offset)
    return width


def read_vint(data: bytes, pos: int, *, kind: str) -> tuple[int, int, bool]:
    """Read an EBML variable-length *size* integer (marker stripped).

    Returns ``(value, width, is_unknown)``. ``is_unknown`` means every data
    bit is set (the unknown-size sentinel). Element IDs use a different rule
    (the marker is retained); see :func:`read_element_id`.
    """
    if pos >= len(data):
        raise TruncatedInputError(f"truncated {kind} VINT", pos)
    width = _vint_width(data[pos], pos)
    if pos + width > len(data):
        raise TruncatedInputError(f"truncated {kind} VINT", pos)
    # The marker occupies the leading (9-width) bits of the first octet;
    # keep only the low (8-width) data bits there.
    first_data_mask = 0xFF >> width
    value = data[pos] & first_data_mask
    for octet in data[pos + 1:pos + width]:
        value = (value << 8) | octet
    all_ones = (1 << (7 * width)) - 1
    return value, width, value == all_ones


def read_element_id(data: bytes, pos: int) -> tuple[int, int]:
    """Read an EBML Element ID, retaining its length marker bits.

    Returns ``(element_id, width)`` where ``element_id`` is the full raw
    class identifier (e.g. ``0x1A45DFA3``) used as the element's identity.
    """
    if pos >= len(data):
        raise TruncatedInputError("truncated element id VINT", pos)
    width = _vint_width(data[pos], pos)
    if pos + width > len(data):
        raise TruncatedInputError("truncated element id VINT", pos)
    element_id = int.from_bytes(data[pos:pos + width], "big")
    data_mask = (1 << (7 * width)) - 1
    if element_id & data_mask == data_mask:
        raise EBMLError("reserved EBML id VINT", pos)
    return element_id, width


@dataclass
class Element:
    """One parsed EBML element with optional children."""

    element_id: int
    start: int
    header_len: int
    size: int | None  # ``None`` marks an unknown-length element
    data_start: int
    end: int
    data: bytes
    children: list["Element"] = field(default_factory=list)

    @property
    def name(self) -> str:
        return element_name(self.element_id)

    @property
    def payload(self) -> bytes:
        return self.data[self.data_start:self.end]

    def find(self, element_id: int) -> "Element | None":
        return next((c for c in self.children if c.element_id == element_id), None)

    def find_all(self, element_id: int) -> list["Element"]:
        return [c for c in self.children if c.element_id == element_id]

    def as_uint(self) -> int:
        return int.from_bytes(self.payload, "big", signed=False)

    def as_sint(self) -> int:
        return int.from_bytes(self.payload, "big", signed=True)

    def as_float(self) -> float:
        if self.size == 4:
            return __import__("struct").unpack(">f", self.payload)[0]
        if self.size == 8:
            return __import__("struct").unpack(">d", self.payload)[0]
        raise EBMLError(f"unsupported float width {self.size}", self.start)

    def as_text(self) -> str:
        return self.payload.decode("utf-8")


def _read_header(data: bytes, pos: int, path: tuple[str, ...]) -> Element:
    ident, id_width = read_element_id(data, pos)
    size, size_width, unknown = read_vint(data, pos + id_width, kind="size")
    header_len = id_width + size_width
    data_start = pos + header_len
    if not unknown:
        end = data_start + size
        if end > len(data):
            raise TruncatedInputError(
                f"element {element_name(ident)} ends past buffer", pos, path)
    else:
        end = -1  # resolved while parsing children
    return Element(ident, pos, header_len, None if unknown else size,
                   data_start, end, data)


def _parse_children(data: bytes, start: int, limit: int,
                    stop_ids: frozenset[int],
                    path: tuple[str, ...]) -> list[Element]:
    """Parse children within ``[start, limit)``.

    ``stop_ids`` is non-empty only when enclosing an unknown-length parent:
    a recognised sibling id then marks the parent's end. For known-length
    containers and the document root it is empty, so every element is read.
    """
    if limit > len(data):
        raise TruncatedInputError("declared parent ends past buffer", start, path)
    elements: list[Element] = []
    pos = start
    while pos < limit:
        element = _read_header(data, pos, path)
        if stop_ids and element.element_id in stop_ids:
            break
        child_path = path + (element.name,)
        if element.size is None:
            if element.element_id not in CONTAINERS:
                raise EBMLError(
                    "unknown-length VINT only legal on master elements",
                    pos, child_path)
            # The unknown-length parent ends at its next sibling; if no
            # boundary set is known the buffer/parent limit applies.
            boundary = _BOUNDARIES.get(element.element_id, stop_ids)
            element.children = _parse_children(
                data, element.data_start, limit, boundary, child_path)
            element.end = (element.children[-1].end
                           if element.children else element.data_start)
        else:
            element.end = element.data_start + element.size
            if element.end > limit:
                raise EBMLError("element exceeds parent boundary",
                                element.start, child_path)
            if element.element_id in CONTAINERS:
                element.children = _parse_children(
                    data, element.data_start, element.end,
                    frozenset(), child_path)
        elements.append(element)
        pos = element.end
    return elements


def parse_tree(data: bytes) -> list[Element]:
    """Parse a complete EBML document into a list of root elements."""
    return _parse_children(data, 0, len(data), frozenset(), ())


def count_elements(roots: list[Element]) -> int:
    """Count every element in a parsed tree (roots included)."""
    total = 0
    stack = list(roots)
    while stack:
        element = stack.pop()
        total += 1
        stack.extend(element.children)
    return total
