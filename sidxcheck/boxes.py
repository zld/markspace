"""Boundary-checked ISO BMFF box traversal.

Only box headers and container structure are read here; payloads are fetched
by the callers on demand so that media data is never loaded wholesale.
"""

from __future__ import annotations

import struct
from dataclasses import dataclass, field
from pathlib import Path
from typing import BinaryIO, Iterator

from .errors import BoxLimitError, IOErrorFailure, MediaStructureError, UnsupportedFeatureError

CONTAINER_TYPES = frozenset(
    {"moov", "trak", "mdia", "minf", "stbl", "edts", "dinf", "mvex", "moof", "traf", "mfra"}
)

# Classic (non-fragmented) MP4 only. These boxes announce structures this tool
# deliberately refuses rather than silently mis-indexing them.
FRAGMENT_MARKERS = frozenset({"moof", "mvex", "styp"})
ENCRYPTION_MARKERS = frozenset({"senc", "saiz", "saio", "pssh", "tenc"})

DEFAULT_MAX_DEPTH = 16
DEFAULT_MAX_BOXES = 200_000
MAX_METADATA_BOX = 256 * 1024 * 1024


@dataclass(frozen=True)
class Box:
    type: str
    start: int
    header_size: int
    end: int
    depth: int
    path: str
    parent_path: str = ""

    @property
    def size(self) -> int:
        return self.end - self.start

    @property
    def payload_start(self) -> int:
        return self.start + self.header_size

    @property
    def payload_size(self) -> int:
        return self.end - self.payload_start

    def __str__(self) -> str:  # pragma: no cover - diagnostics only
        return f"{self.path or self.type}@{self.start}+{self.size}"


@dataclass
class BoxTree:
    file_size: int
    boxes: list[Box] = field(default_factory=list)
    by_type: dict[str, list[Box]] = field(default_factory=dict)
    top_level: list[Box] = field(default_factory=list)

    def add(self, box: Box) -> None:
        self.boxes.append(box)
        self.by_type.setdefault(box.type, []).append(box)
        if box.depth == 0:
            self.top_level.append(box)

    def all(self, box_type: str) -> list[Box]:
        return list(self.by_type.get(box_type, ()))

    def first(self, box_type: str, *, required: bool = False, unique: bool = False) -> Box | None:
        matches = self.by_type.get(box_type, ())
        if unique and len(matches) > 1:
            raise MediaStructureError(
                f"expected a single {box_type!r} box, found {len(matches)}", location=box_type
            )
        if not matches:
            if required:
                raise MediaStructureError(f"missing required {box_type!r} box", location=box_type)
            return None
        return matches[0]

    def children(self, parent: Box) -> list[Box]:
        return [b for b in self.boxes if b.parent_path == parent.path]

    def find_path(self, path: str) -> Box | None:
        for box in self.boxes:
            if box.path == path:
                return box
        return None

    def require_path(self, path: str) -> Box:
        box = self.find_path(path)
        if box is None:
            raise MediaStructureError(f"missing {path!r}", location=path)
        return box


@dataclass(frozen=True)
class ParseLimits:
    max_depth: int = DEFAULT_MAX_DEPTH
    max_boxes: int = DEFAULT_MAX_BOXES


def read_exact(fp: BinaryIO, offset: int, length: int) -> bytes:
    """Read exactly *length* bytes at *offset* or fail with a structural error."""
    if length < 0:
        raise MediaStructureError(f"negative read length {length}", location=f"offset {offset}")
    fp.seek(offset)
    data = fp.read(length)
    if len(data) != length:
        raise MediaStructureError(
            f"truncated read: wanted {length} bytes, got {len(data)}",
            location=f"offset {offset}",
        )
    return data


def read_payload(fp: BinaryIO, box: Box, *, limit: int = MAX_METADATA_BOX) -> bytes:
    if box.payload_size > limit:
        raise BoxLimitError(
            f"{box.path} payload {box.payload_size} exceeds metadata limit {limit}",
            location=f"{box.path}@{box.start}",
        )
    return read_exact(fp, box.payload_start, box.payload_size)


def _display_type(raw: bytes) -> str:
    # ISO BMFF metadata boxes may use 0xA9-prefixed latin-1 names in udta.
    try:
        name = raw.decode("ascii")
    except UnicodeDecodeError:
        name = raw.decode("latin-1")
    if any(ord(ch) < 0x20 or ch == " " for ch in name):
        raise MediaStructureError(f"invalid box type {raw!r}")
    return name


def _box_path(parent_path: str, box_type: str, occurrence: int) -> str:
    name = box_type if occurrence == 0 else f"{box_type}#{occurrence}"
    return f"{parent_path}/{name}" if parent_path else name


def _parse_level(
    fp: BinaryIO,
    tree: BoxTree,
    start: int,
    end: int,
    depth: int,
    parent_path: str,
    limits: ParseLimits,
) -> Iterator[Box]:
    if depth > limits.max_depth:
        raise BoxLimitError(
            f"box nesting depth exceeded {limits.max_depth}", location=parent_path or "root"
        )
    counts: dict[str, int] = {}
    position = start
    while position < end:
        remaining = end - position
        if remaining < 8:
            raise MediaStructureError(
                f"{remaining} trailing byte(s) cannot hold a box header",
                location=f"{parent_path or 'root'} offset {position}",
            )
        header = read_exact(fp, position, 16 if remaining >= 16 else 8)
        size32, raw_type = struct.unpack(">I4s", header[:8])
        try:
            # The type is needed for location messages even when malformed.
            name = _display_type(raw_type)
        except MediaStructureError as exc:
            raise exc.__class__(exc.message, location=f"offset {position}") from None
        header_size = 8
        if size32 == 1:
            if remaining < 16:
                raise MediaStructureError(
                    f"truncated 64-bit size header for {name!r}",
                    location=f"offset {position}",
                )
            (size64,) = struct.unpack(">Q", header[8:16])
            if size64 < 16:
                raise MediaStructureError(
                    f"{name!r} has invalid largesize {size64}", location=f"offset {position}"
                )
            box_size = size64
            header_size = 16
        elif size32 == 0:
            box_size = remaining
        elif size32 < 8:
            raise MediaStructureError(
                f"{name!r} has invalid size {size32} (must be 0, 1 or >= 8)",
                location=f"offset {position}",
            )
        else:
            box_size = size32
        if box_size > remaining:
            raise MediaStructureError(
                f"{name!r} size {box_size} overruns enclosing box by {box_size - remaining} byte(s)",
                location=f"offset {position}",
            )
        if box_size < header_size:
            raise MediaStructureError(
                f"{name!r} size {box_size} is smaller than its {header_size}-byte header",
                location=f"offset {position}",
            )
        occurrence = counts.get(name, 0)
        counts[name] = occurrence + 1
        path = _box_path(parent_path, name, occurrence)
        box = Box(
            type=name,
            start=position,
            header_size=header_size,
            end=position + box_size,
            depth=depth,
            path=path,
            parent_path=parent_path,
        )
        tree.add(box)
        if len(tree.boxes) > limits.max_boxes:
            raise BoxLimitError(f"box count exceeded limit {limits.max_boxes}", location=path)
        if name in FRAGMENT_MARKERS:
            raise UnsupportedFeatureError(
                f"{name!r} indicates fragmented MP4, which is not supported", location=path
            )
        if name in ENCRYPTION_MARKERS:
            raise UnsupportedFeatureError(
                f"{name!r} indicates encrypted or protected samples, which are not supported",
                location=path,
            )
        yield box
        if name in CONTAINER_TYPES:
            yield from _parse_level(
                fp, tree, box.payload_start, box.end, depth + 1, path, limits
            )
        position += box_size


def parse_boxes(
    fp: BinaryIO,
    file_size: int,
    *,
    max_depth: int = DEFAULT_MAX_DEPTH,
    max_boxes: int = DEFAULT_MAX_BOXES,
) -> BoxTree:
    """Parse the top-level boxes and known containers, rejecting bad bounds."""
    if file_size < 8:
        raise MediaStructureError(
            f"file is too small to be an MP4 ({file_size} bytes)", location="file"
        )
    if max_depth < 0 or max_boxes < 1:
        raise ValueError("parse limits must be positive")
    limits = ParseLimits(max_depth=max_depth, max_boxes=max_boxes)
    tree = BoxTree(file_size=file_size)
    for _ in _parse_level(fp, tree, 0, file_size, 0, "", limits):
        pass
    return tree


def parse_file(
    path: Path,
    *,
    max_depth: int = DEFAULT_MAX_DEPTH,
    max_boxes: int = DEFAULT_MAX_BOXES,
) -> BoxTree:
    try:
        with path.open("rb") as fp:
            return parse_boxes(
                fp, path.stat().st_size, max_depth=max_depth, max_boxes=max_boxes
            )
    except OSError as exc:
        raise IOErrorFailure(f"cannot read {path}: {exc}", location=str(path)) from exc
