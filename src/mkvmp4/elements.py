"""EBML element tree: bounds, unknown-size elements, and summaries."""
from __future__ import annotations

from dataclasses import dataclass, field

from .ebml import UNKNOWN_SIZE, EbmlError, read_header

MAX_DEPTH = 16
MAX_ELEMENT_SIZE = 1 << 34  # 16 GiB hard cap on a single element payload

ID_EBML_HEADER = 0x1A45DFA3
ID_SEGMENT = 0x18538067
ID_CLUSTER = 0x1F43B675
ID_SIMPLE_BLOCK = 0xA3
ID_BLOCK = 0xA1

# Only Segment and Cluster may legally use unknown (all-ones) sizes.
UNKNOWN_SIZE_ALLOWED = {ID_SEGMENT, ID_CLUSTER}

# IDs that terminate an unknown-size Cluster: its parent's other children.
CLUSTER_STOP_IDS = {
    ID_CLUSTER, 0x1549A966, 0x1654AE6B, 0x1C53BB6B, 0x1043A770,
    0x1941A469, 0x1254C367, 0x7373, 0xEC, 0xBF,
}

# Master elements we recurse into; everything else is treated as opaque.
MASTER_IDS = {
    ID_SEGMENT, ID_CLUSTER, 0x1549A966, 0x1654AE6B,  # Segment, Info, Tracks
    0xAE, 0xE0, 0xE1, 0xA0,  # TrackEntry, Video, Audio, BlockGroup
}


@dataclass
class Element:
    id: int
    offset: int       # file offset of the element header
    header_size: int
    size: int         # payload size; unknown sizes are resolved during parse
    unknown_size: bool = False
    children: list["Element"] = field(default_factory=list)

    @property
    def data_offset(self) -> int:
        return self.offset + self.header_size

    @property
    def end(self) -> int:
        return self.data_offset + self.size


def _parse_level(data: bytes, start: int, end: int, depth: int,
                 stop_ids: frozenset | set) -> list[Element]:
    if depth > MAX_DEPTH:
        raise EbmlError(f"element nesting exceeds {MAX_DEPTH} levels", start)
    elements: list[Element] = []
    pos = start
    while pos < end:
        eid, size, hsize = read_header(data, pos)
        if eid in stop_ids:
            break  # belongs to an ancestor level; unknown-size sibling ends here
        el = Element(eid, pos, hsize, 0)
        if size == UNKNOWN_SIZE:
            if eid not in UNKNOWN_SIZE_ALLOWED:
                raise EbmlError(
                    f"element 0x{eid:X} uses unknown size but is not Segment/Cluster",
                    pos)
            el.unknown_size = True
            child_stops = CLUSTER_STOP_IDS if eid == ID_CLUSTER else set()
            el.children = _parse_level(data, el.data_offset, end, depth + 1,
                                       child_stops)
            last = el.data_offset
            for child in el.children:
                last = max(last, child.end)
            el.size = last - el.data_offset  # boundary: last child or parent end
        else:
            if size > MAX_ELEMENT_SIZE:
                raise EbmlError(f"element size {size} exceeds safety limit", pos)
            if el.data_offset + size > end or el.data_offset + size > len(data):
                raise EbmlError("element extends beyond its parent bounds", pos)
            el.size = size
            if eid in MASTER_IDS:
                el.children = _parse_level(data, el.data_offset, el.end,
                                           depth + 1, set())
        pos = el.end
        elements.append(el)
    return elements


def parse_file(data: bytes) -> list[Element]:
    """Parse top-level elements (EBML header, Segment) of a Matroska file."""
    return _parse_level(data, 0, len(data), 0, set())


def summarize(elements: list[Element]) -> dict:
    """Count structural elements; block payloads are reported separately."""
    count = 0
    blocks = 0

    def walk(els: list[Element]) -> None:
        nonlocal count, blocks
        for el in els:
            if el.id in (ID_SIMPLE_BLOCK, ID_BLOCK):
                blocks += 1
                continue
            count += 1
            walk(el.children)

    walk(elements)
    return {"element_count": count, "block_elements": blocks}
