"""Element tree summaries with bounded recursion and unknown-size handling.

The tree is the single source of structural truth used by both ``inspect``
and the remux path.  Parsing is recursive but the recursion is hard-limited
by ``max_depth`` before descending, and the total element count is bounded by
``max_elements``, so hostile nesting cannot exhaust interpreter resources.
Unknown-size Segment and Cluster elements are bounded by the rules in
``_BOUNDARY_STOPPERS``; anything else with an unknown size is an error.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Iterator

from .ebml import ElementHeader, read_element_header, read_vint_id
from .errors import BoundaryError, EbmlParseError

# ---------------------------------------------------------------------------
# Element ID registry (Matroska subset this tool understands structurally)
# ---------------------------------------------------------------------------

ELEMENT_NAMES: dict[int, str] = {
    0x1A45DFA3: "EBML",
    0x4286: "EBMLVersion",
    0x42F7: "EBMLReadVersion",
    0x42F2: "EBMLMaxIDLength",
    0x42F3: "EBMLMaxSizeLength",
    0x4282: "DocType",
    0x4287: "DocTypeVersion",
    0x4285: "DocTypeReadVersion",
    0x18538067: "Segment",
    0x1549A966: "Info",
    0x2AD7B1: "TimestampScale",
    0x4D80: "MuxingApp",
    0x5741: "WritingApp",
    0x1654AE6B: "Tracks",
    0xAE: "TrackEntry",
    0xD7: "TrackNumber",
    0x73C5: "TrackUID",
    0x83: "TrackType",
    0x86: "CodecID",
    0x63A2: "CodecPrivate",
    0x23E383: "DefaultDuration",
    0xB9: "FlagEnabled",
    0x88: "FlagDefault",
    0x55AA: "FlagForced",
    0x9C: "FlagLacing",
    0x536E: "Name",
    0x22B59C: "Language",
    0xE1: "Audio",
    0xB5: "SamplingFrequency",
    0x9F: "Channels",
    0xE0: "Video",
    0xB0: "PixelWidth",
    0xBA: "PixelHeight",
    0x1F43B675: "Cluster",
    0xE7: "Timestamp",
    0xA3: "SimpleBlock",
    0xA0: "BlockGroup",
    0xA1: "Block",
    0x9B: "BlockDuration",
    0x75A1: "BlockAdditions",
    0x1C53BB6B: "Cues",
    0xBB: "CuePoint",
    0xB3: "CueTime",
    0xB7: "CueTrackPositions",
    0xF7: "CueTrack",
    0xF1: "CueClusterPosition",
    0x1254C367: "Tags",
    0x114D9B74: "SeekHead",
    0x4DBB: "Seek",
    0x53AB: "SeekID",
    0x53AC: "SeekPosition",
    0xEC: "Void",
    0xBF: "CRC-32",
}

# Containers whose children are elements.  Unknown IDs are treated as leaves.
MASTER_ELEMENTS: frozenset[int] = frozenset(
    {
        0x1A45DFA3,  # EBML
        0x18538067,  # Segment
        0x1549A966,  # Info
        0x1654AE6B,  # Tracks
        0xAE,  # TrackEntry
        0xE1,  # Audio
        0xE0,  # Video
        0x1F43B675,  # Cluster
        0xA0,  # BlockGroup
        0x75A1,  # BlockAdditions
        0x1C53BB6B,  # Cues
        0xBB,  # CuePoint
        0xB7,  # CueTrackPositions
        0x1254C367,  # Tags
        0x114D9B74,  # SeekHead
        0x4DBB,  # Seek
    }
)

# Elements whose unknown size can be bounded by the start of a known sibling.
UNKNOWN_SIZE_ALLOWED: frozenset[int] = frozenset(
    {
        0x18538067,  # Segment
        0x1F43B675,  # Cluster
    }
)

# A Segment's direct children.  Each one terminates an unknown-size Cluster.
SEGMENT_CHILD_IDS: frozenset[int] = frozenset(
    {
        0x114D9B74,  # SeekHead
        0x1549A966,  # Info
        0x1654AE6B,  # Tracks
        0x1F43B675,  # Cluster
        0x1C53BB6B,  # Cues
        0x1254C367,  # Tags
    }
)

_BOUNDARY_STOPPERS: dict[int, frozenset[int]] = {
    0x1F43B675: SEGMENT_CHILD_IDS,  # Cluster ends at the next Segment child
    0x18538067: frozenset({0x18538067, 0x1A45DFA3}),  # next Segment/EBML header
}


def element_name(element_id: int) -> str:
    return ELEMENT_NAMES.get(element_id, f"Unknown(0x{element_id:X})")


# ---------------------------------------------------------------------------
# Raw tree nodes
# ---------------------------------------------------------------------------


@dataclass
class RawNode:
    """One element with its absolute byte range and decoded header."""

    element_id: int
    name: str
    level: int
    offset: int
    data_offset: int
    end_offset: int
    size: int
    unknown_size: bool
    path: str
    children: list["RawNode"] = field(default_factory=list)

    @property
    def byte_length(self) -> int:
        return self.end_offset - self.offset

    @property
    def data_length(self) -> int:
        return self.end_offset - self.data_offset

    def walk(self) -> Iterator["RawNode"]:
        yield self
        for child in self.children:
            yield from child.walk()

    def count(self) -> int:
        return sum(1 for _ in self.walk())


@dataclass
class SummaryNode:
    """Serialisable summary row: id, name, level, byte range, child count."""

    element_id: int
    name: str
    level: int
    offset: int
    data_offset: int
    end_offset: int
    size: int
    unknown_size: bool
    path: str
    child_count: int

    def to_dict(self) -> dict[str, Any]:
        return {
            "id": self.element_id,
            "id_hex": f"0x{self.element_id:X}",
            "name": self.name,
            "level": self.level,
            "offset": self.offset,
            "data_offset": self.data_offset,
            "end_offset": self.end_offset,
            "size": self.size,
            "unknown_size": self.unknown_size,
            "path": self.path,
            "children": self.child_count,
        }


@dataclass
class TreeSummary:
    nodes: list[SummaryNode]
    max_depth: int
    total_count: int
    truncated: bool
    unknown_sizes: int

    def to_dict(self) -> dict[str, Any]:
        return {
            "node_count": len(self.nodes),
            "total_count": self.total_count,
            "max_depth": self.max_depth,
            "truncated": self.truncated,
            "unknown_sizes": self.unknown_sizes,
            "nodes": [n.to_dict() for n in self.nodes],
        }

    def find(self, name: str) -> Iterator[SummaryNode]:
        return (n for n in self.nodes if n.name == name)


def _node_path(parent_path: str, name: str) -> str:
    return f"{parent_path}/{name}" if parent_path else f"/{name}"


def _truncate_path(path: str, keep: int = 5) -> str:
    """Show the tail of a long path, which is what locates an error."""
    parts = path.strip("/").split("/")
    if len(parts) <= keep:
        return "/" + "/".join(parts)
    return "/.../" + "/".join(parts[-keep:])


class _Budget:
    __slots__ = ("count", "max_count", "max_depth", "unknown_sizes")

    def __init__(self, max_count: int, max_depth: int) -> None:
        self.count = 0
        self.max_count = max_count
        self.max_depth = max_depth
        self.unknown_sizes = 0

    def spend(self) -> None:
        self.count += 1


def _peek_element_id(data: bytes, pos: int, limit: int) -> int | None:
    if pos >= limit:
        return None
    try:
        element_id, length = read_vint_id(data, pos)
    except EbmlParseError:
        return None
    if pos + length > limit:
        return None
    return element_id


def _parse_children(
    data: bytes,
    start: int,
    limit: int,
    level: int,
    parent_path: str,
    *,
    base: int,
    budget: _Budget,
    stop_ids: frozenset[int] | None,
    out: list[RawNode],
) -> int:
    """Parse elements in ``[start, limit)`` and return the stop position.

    ``stop_ids`` is set for unknown-size containers: the first element whose
    ID is a stopper is not consumed and its offset is returned.
    """
    pos = start
    while pos < limit:
        if budget.count >= budget.max_count:
            raise BoundaryError(
                f"element budget of {budget.max_count} exhausted; refusing to parse "
                "further (possible hostile nesting)",
                offset=base + pos,
                path=_truncate_path(parent_path),
            )
        if stop_ids is not None:
            peeked = _peek_element_id(data, pos, limit)
            if peeked in stop_ids:
                return pos
        header: ElementHeader = read_element_header(
            data, pos, base=base, path=_truncate_path(parent_path), limit=limit
        )
        name = element_name(header.element_id)
        node_path = _node_path(parent_path, name)
        if level > budget.max_depth:
            raise BoundaryError(
                f"maximum nesting depth {budget.max_depth} exceeded",
                offset=base + pos,
                path=_truncate_path(node_path),
            )
        unknown = header.size is None
        if unknown and header.element_id not in UNKNOWN_SIZE_ALLOWED:
            raise BoundaryError(
                f"element {name} has unknown size and no rule can bound it",
                offset=base + pos,
                path=_truncate_path(node_path),
                element_name=name,
            )
        budget.spend()
        if unknown:
            budget.unknown_sizes += 1
            child_limit = limit
            child_stoppers: frozenset[int] | None = _BOUNDARY_STOPPERS.get(
                header.element_id, frozenset()
            )
        else:
            assert header.size is not None
            child_limit = header.data_offset + header.size
            child_stoppers = None

        node = RawNode(
            element_id=header.element_id,
            name=name,
            level=level,
            offset=pos,
            data_offset=header.data_offset,
            end_offset=child_limit,
            size=child_limit - header.data_offset,
            unknown_size=unknown,
            path=node_path,
        )
        out.append(node)

        is_master = header.element_id in MASTER_ELEMENTS
        if not is_master:
            pos = child_limit
            continue
        if child_limit <= header.data_offset and not unknown:
            pos = child_limit
            continue

        children: list[RawNode] = []
        node.children = children
        stop_pos = _parse_children(
            data,
            header.data_offset,
            child_limit,
            level + 1,
            node_path,
            base=base,
            budget=budget,
            stop_ids=child_stoppers,
            out=children,
        )
        if unknown:
            # The unknown-size container ends where its stopper begins (or at
            # the enclosing limit); the true byte range is now known.
            node.end_offset = stop_pos
            node.size = stop_pos - node.data_offset
            pos = stop_pos
        else:
            pos = child_limit
    return pos


def parse_tree(
    data: bytes,
    *,
    base: int = 0,
    start: int = 0,
    end: int | None = None,
    max_depth: int = 64,
    max_elements: int = 200_000,
) -> tuple[list[RawNode], dict[str, Any]]:
    """Parse ``data[start:end]`` into a raw element tree.

    Returns top-level nodes plus statistics.  Raises ``BoundaryError`` when an
    unknown-size element cannot be bounded or a budget is exceeded, and
    ``EbmlParseError`` for malformed VINTs or overrunning sizes.
    """
    if end is None:
        end = len(data)
    if start < 0 or end > len(data) or start > end:
        raise BoundaryError(
            f"invalid parse region [{start}, {end}) for {len(data)} bytes",
            offset=base + start,
        )
    budget = _Budget(max_elements, max_depth)
    top: list[RawNode] = []
    _parse_children(
        data,
        start,
        end,
        0,
        "",
        base=base,
        budget=budget,
        stop_ids=None,
        out=top,
    )
    stats: dict[str, Any] = {
        "total_count": budget.count,
        "max_depth": max(
            (n.level for root in top for n in root.walk()), default=-1
        ),
        "unknown_sizes": budget.unknown_sizes,
        "truncated": False,
    }
    return top, stats


def summarize(
    nodes: list[RawNode],
    stats: dict[str, Any],
    *,
    max_nodes: int = 10_000,
) -> TreeSummary:
    """Flatten a raw tree into serialisable summary rows.

    ``max_nodes`` caps the number of reported rows so a huge but legal file
    still produces bounded JSON; ``truncated`` says the report is partial.
    """
    rows: list[SummaryNode] = []
    truncated = False
    unknown = 0
    total = 0
    for root in nodes:
        for item in root.walk():
            total += 1
            unknown += int(item.unknown_size)
            if len(rows) >= max_nodes:
                truncated = True
                continue
            rows.append(
                SummaryNode(
                    element_id=item.element_id,
                    name=item.name,
                    level=item.level,
                    offset=item.offset,
                    data_offset=item.data_offset,
                    end_offset=item.end_offset,
                    size=item.size,
                    unknown_size=item.unknown_size,
                    path=item.path,
                    child_count=len(item.children),
                )
            )
    return TreeSummary(
        nodes=rows,
        max_depth=stats.get("max_depth", 0),
        total_count=total,
        truncated=truncated,
        unknown_sizes=unknown,
    )
