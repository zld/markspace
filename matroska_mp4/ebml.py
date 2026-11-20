"""Bounded EBML parsing: VINTs, element tree, unknown-size boundaries.

The parser never keeps media payloads in memory.  It seeks over leaf payloads
and records only structural metadata (offsets, sizes, parent/child links).
"""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from typing import BinaryIO, Iterator

from . import ebml_ids as ids

MAX_INPUT_BYTES = 8 * 1024 * 1024 * 1024
MAX_ELEMENT_DEPTH = 64


class EBMLError(Exception):
    """Malformed EBML; always carries the byte offset of the failure."""

    def __init__(self, offset: int, message: str):
        self.offset = offset
        self.message = message
        super().__init__(f"EBML error at offset {offset}: {message}")


@dataclass(slots=True)
class Vint:
    value: int
    length: int
    unknown: bool = False
    raw: bytes = b""


@dataclass(slots=True)
class Header:
    element_id: int
    id_length: int
    size: int | None          # None means unknown size
    size_length: int
    data_offset: int
    offset: int
    unknown: bool

    @property
    def header_size(self) -> int:
        return self.id_length + self.size_length


@dataclass(slots=True)
class Element:
    element_id: int
    offset: int
    header_size: int
    data_offset: int
    size: int | None
    end: int | None
    unknown: bool
    children: list["Element"] = field(default_factory=list)
    parent_id: int | None = None

    @property
    def name(self) -> str:
        return ids.element_name(self.element_id)

    def iter_descendants(self) -> Iterator["Element"]:
        for child in self.children:
            yield child
            yield from child.iter_descendants()

    def find(self, element_id: int) -> "Element | None":
        for child in self.children:
            if child.element_id == element_id:
                return child
        return None

    def children_of(self, element_id: int) -> list["Element"]:
        return [c for c in self.children if c.element_id == element_id]


def _read_exact(fh: BinaryIO, offset: int, count: int, limit: int) -> bytes:
    if count < 0 or offset < 0:
        raise EBMLError(offset, "negative read")
    if offset + count > limit:
        raise EBMLError(offset, f"truncated: need {count} bytes, "
                                f"only {max(0, limit - offset)} remain")
    fh.seek(offset)
    data = fh.read(count)
    if len(data) != count:
        raise EBMLError(offset, f"truncated: need {count} bytes, got {len(data)}")
    return data


def read_vint(fh: BinaryIO, offset: int, limit: int, *, for_id: bool) -> Vint:
    """Read one EBML VINT and reject invalid encodings with their offset."""

    first = _read_exact(fh, offset, 1, limit)[0]
    if first == 0:
        raise EBMLError(offset, "invalid VINT: leading zero byte")
    length = 1
    mask = 0x80
    while not (first & mask):
        mask >>= 1
        length += 1
    if for_id:
        if length > 4:
            raise EBMLError(offset, f"invalid element ID VINT length {length}")
        raw = _read_exact(fh, offset, length, limit)
        return Vint(value=int.from_bytes(raw, "big"), length=length, raw=raw)
    if length > 8:
        raise EBMLError(offset, f"invalid size VINT length {length}")
    raw = _read_exact(fh, offset, length, limit)
    value = int.from_bytes(raw, "big") & ((1 << (7 * length)) - 1)
    unknown = value == (1 << (7 * length)) - 1
    return Vint(value=value, length=length, unknown=unknown, raw=raw)


def read_header(fh: BinaryIO, offset: int, limit: int) -> Header:
    if offset >= limit:
        raise EBMLError(offset, "truncated element header")
    id_vint = read_vint(fh, offset, limit, for_id=True)
    size_offset = offset + id_vint.length
    size_vint = read_vint(fh, size_offset, limit, for_id=False)
    data_offset = size_offset + size_vint.length
    size = None if size_vint.unknown else size_vint.value
    if size is not None and data_offset + size > limit:
        raise EBMLError(offset,
                        f"{ids.element_name(id_vint.value)} claims {size} bytes "
                        f"but only {max(0, limit - data_offset)} remain")
    return Header(element_id=id_vint.value, id_length=id_vint.length, size=size,
                  size_length=size_vint.length, data_offset=data_offset,
                  offset=offset, unknown=size_vint.unknown)


def _is_known_master(element_id: int) -> bool:
    return element_id in ids.MASTER_CHILDREN


def _parse_children(fh: BinaryIO, parent: Element, limit: int, level_ids: frozenset[int],
                    depth: int) -> None:
    """Parse children until limit; unknown-size parents end at a sibling ID."""

    if depth > MAX_ELEMENT_DEPTH:
        raise EBMLError(parent.offset, "element nesting deeper than 64")
    cursor = parent.data_offset
    while True:
        if cursor == limit:
            if parent.size is not None and cursor != parent.end:
                raise EBMLError(parent.offset, "element size mismatch")
            if parent.unknown:
                parent.end = cursor
            return
        if cursor > limit:
            raise EBMLError(parent.offset, "child overruns parent boundary")
        header = read_header(fh, cursor, limit)
        if parent.unknown and header.element_id not in level_ids:
            # For unknown-size Segment/Cluster the first non-child terminates it.
            parent.end = cursor
            return
        child = Element(element_id=header.element_id, offset=header.offset,
                        header_size=header.header_size, data_offset=header.data_offset,
                        size=header.size, end=None, unknown=header.unknown,
                        parent_id=parent.element_id)
        if header.unknown and header.element_id not in ids.UNKNOWN_SIZE_IDS:
            raise EBMLError(header.offset,
                            f"unknown-size leaf {child.name} is not allowed")
        if _is_known_master(header.element_id):
            child_limit = limit if header.unknown else header.data_offset + header.size
            _parse_children(fh, child, child_limit, ids.MASTER_CHILDREN[header.element_id],
                            depth + 1)
            if not header.unknown:
                child.end = child_limit
        else:
            if header.unknown:
                raise EBMLError(header.offset, f"unknown-size leaf {child.name}")
            child.end = header.data_offset + header.size
        parent.children.append(child)
        cursor = child.end
        if cursor is None:
            raise EBMLError(child.offset, f"{child.name} has no determined end")
        if parent.size is not None and cursor > parent.end:
            raise EBMLError(child.offset, "child overruns known-size parent")


@dataclass(slots=True)
class Document:
    path: str
    file_size: int
    roots: list[Element]

    def find_root(self, element_id: int) -> Element | None:
        for root in self.roots:
            if root.element_id == element_id:
                return root
        return None

    def counts(self) -> dict[str, int]:
        counts: dict[str, int] = {}
        for root in self.roots:
            for element in (root, *root.iter_descendants()):
                counts[element.name] = counts.get(element.name, 0) + 1
        return counts

    def unknown_sizes(self) -> dict[str, int]:
        sizes: dict[str, int] = {}
        for root in self.roots:
            for element in (root, *root.iter_descendants()):
                if element.unknown:
                    sizes[element.name] = sizes.get(element.name, 0) + 1
        return sizes


def parse_file(path: str) -> Document:
    """Parse an EBML document from disk with strict bounds checks."""

    if not os.path.isfile(path):
        raise FileNotFoundError(path)
    file_size = os.path.getsize(path)
    if file_size > MAX_INPUT_BYTES:
        raise EBMLError(0, f"input is {file_size} bytes; limit is {MAX_INPUT_BYTES}")
    with open(path, "rb") as fh:
        roots = _parse_roots(fh, file_size)
    return Document(path=path, file_size=file_size, roots=roots)


def _parse_roots(fh: BinaryIO, limit: int) -> list[Element]:
    roots: list[Element] = []
    cursor = 0
    seen_segment = False
    while cursor < limit:
        header = read_header(fh, cursor, limit)
        root = Element(element_id=header.element_id, offset=header.offset,
                       header_size=header.header_size, data_offset=header.data_offset,
                       size=header.size, end=None, unknown=header.unknown,
                       parent_id=None)
        if header.element_id not in ids.MASTER_CHILDREN:
            raise EBMLError(header.offset,
                            f"top-level element {root.name} must be a master")
        if header.unknown and header.element_id not in ids.UNKNOWN_SIZE_IDS:
            raise EBMLError(header.offset, f"unknown-size top-level {root.name}")
        if header.unknown:
            child_limit = limit
        else:
            child_limit = header.data_offset + header.size
        _parse_children(fh, root, child_limit,
                        ids.MASTER_CHILDREN[header.element_id], 0)
        if not header.unknown:
            root.end = child_limit
        if root.element_id == ids.SEGMENT:
            if seen_segment:
                raise EBMLError(root.offset, "multiple Segment roots")
            seen_segment = True
        if root.end is None or root.end <= cursor:
            raise EBMLError(root.offset, f"{root.name} has no determined end")
        roots.append(root)
        cursor = root.end
    if not roots:
        raise EBMLError(0, "empty EBML document")
    if roots[0].element_id != ids.EBML:
        raise EBMLError(roots[0].offset, "document does not start with EBML header")
    if not seen_segment:
        raise EBMLError(roots[-1].end or limit, "missing Segment")
    return roots


def read_leaf(fh: BinaryIO, element: Element) -> bytes:
    """Read a small metadata leaf (never used for media payloads)."""

    if element.size is None or element.end is None:
        raise EBMLError(element.offset, f"{element.name} has unknown size")
    if element.size > 64 * 1024 * 1024:
        raise EBMLError(element.offset, f"{element.name} metadata leaf too large")
    fh.seek(element.data_offset)
    data = fh.read(element.size)
    if len(data) != element.size:
        raise EBMLError(element.offset, f"truncated {element.name} payload")
    return data


def uint_value(data: bytes) -> int:
    return int.from_bytes(data, "big", signed=False) if data else 0


def int_value(data: bytes) -> int:
    return int.from_bytes(data, "big", signed=True) if data else 0


def float_value(data: bytes) -> float:
    if len(data) == 4:
        import struct
        return struct.unpack(">f", data)[0]
    if len(data) == 8:
        import struct
        return struct.unpack(">d", data)[0]
    raise ValueError(f"invalid float width {len(data)}")
