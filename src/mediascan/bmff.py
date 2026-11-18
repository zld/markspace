"""Minimal, defensive ISO BMFF (MP4/M4A) box walker.

Only box *structure* is parsed here: sizes, extended sizes, nesting and
boundaries.  Payload interpretation lives in :mod:`mediascan.extract` and the
codec-specific helpers.  Every traversal is bounded by :class:`Limits`.
"""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from typing import BinaryIO

from .errors import (
    ERR_BOX_LENGTH,
    ERR_BOX_LIMIT,
    ERR_DEPTH_LIMIT,
    ERR_IO,
    ERR_TRUNCATED,
    ScanFailure,
)
from .limits import Limits

# Boxes whose payload is itself a sequence of boxes.
CONTAINERS = frozenset(
    {
        b"moov",
        b"trak",
        b"mdia",
        b"minf",
        b"stbl",
        b"edts",
        b"mvex",
        b"moof",
        b"traf",
        b"mfra",
        b"udta",
        b"dinf",
        b"ipro",
        b"sinf",
        b"schi",
    }
)

# Boxes that exist in many files and are irrelevant for metadata; kept in the
# tree as leaves but never descended into.
KNOWN_LEAVES = frozenset(
    {
        b"ftyp",
        b"free",
        b"skip",
        b"mdat",
        b"mvhd",
        b"tkhd",
        b"mdhd",
        b"hdlr",
        b"vmhd",
        b"smhd",
        b"dref",
        b"stsd",
        b"stts",
        b"stsc",
        b"stsz",
        b"stco",
        b"co64",
        b"stss",
        b"ctts",
        b"elst",
        b"mehd",
        b"trex",
        b"mfhd",
        b"tfhd",
        b"tfdt",
        b"trun",
        b"meta",
        b"ilst",
    }
)


@dataclass
class Box:
    type: str
    offset: int
    size: int  # total size including header, 0 => to EOF
    header_size: int
    payload_offset: int
    payload_size: int
    depth: int
    children: list["Box"] = field(default_factory=list)

    @property
    def end(self) -> int:
        return self.payload_offset + self.payload_size


def read_exact(fh: BinaryIO, n: int, offset: int) -> bytes:
    try:
        fh.seek(offset)
        data = fh.read(n)
    except OSError as exc:  # pragma: no cover - platform dependent
        raise ScanFailure(ERR_IO, f"read failed at {offset}: {exc}", offset) from exc
    if len(data) != n:
        raise ScanFailure(
            ERR_TRUNCATED,
            f"expected {n} bytes at offset {offset}, got {len(data)}",
            offset,
        )
    return data


class BoxReader:
    def __init__(self, fh: BinaryIO, file_size: int, limits: Limits | None = None):
        self.fh = fh
        self.file_size = file_size
        self.limits = limits or Limits()
        self.count = 0

    # -- public ---------------------------------------------------------
    def parse(self) -> list[Box]:
        return self._parse_range(0, self.file_size, depth=0, top_level=True)

    def find(self, boxes: list[Box], path: tuple[str, ...]) -> Box | None:
        for box in boxes:
            if box.type == path[0]:
                if len(path) == 1:
                    return box
                return self.find(box.children, path[1:])
        return None

    # -- internals ------------------------------------------------------
    def _parse_range(
        self, start: int, end: int, depth: int, top_level: bool = False
    ) -> list[Box]:
        if depth > self.limits.max_depth:
            raise ScanFailure(
                ERR_DEPTH_LIMIT,
                f"box nesting exceeds {self.limits.max_depth}",
                start,
            )
        boxes: list[Box] = []
        pos = start
        while pos < end:
            remaining = end - pos
            if remaining < 8:
                if remaining == 0:
                    break
                raise ScanFailure(
                    ERR_TRUNCATED,
                    f"{remaining} trailing bytes cannot hold a box header",
                    pos,
                )
            head = read_exact(self.fh, 8, pos)
            size = int.from_bytes(head[0:4], "big")
            btype = head[4:8]
            header = 8
            if size == 1:
                if remaining < 16:
                    raise ScanFailure(
                        ERR_TRUNCATED,
                        "64-bit box size field is truncated",
                        pos,
                    )
                size = int.from_bytes(read_exact(self.fh, 8, pos + 8), "big")
                header = 16
            elif size == 0:
                if not top_level:
                    raise ScanFailure(
                        ERR_BOX_LENGTH,
                        "zero-sized box is only valid at top level",
                        pos,
                    )
                size = remaining
            if btype == b"uuid":
                if remaining < header + 16:
                    raise ScanFailure(
                        ERR_TRUNCATED, "uuid box usertype is truncated", pos
                    )
                header += 16
            if size < header:
                raise ScanFailure(
                    ERR_BOX_LENGTH,
                    f"{_tname(btype)} box size {size} smaller than header {header}",
                    pos,
                )
            if size > remaining:
                raise ScanFailure(
                    ERR_BOX_LENGTH,
                    f"{_tname(btype)} box size {size} exceeds boundary "
                    f"({remaining} bytes left)",
                    pos,
                )
            payload_offset = pos + header
            payload_size = size - header
            self.count += 1
            if self.count > self.limits.max_boxes:
                raise ScanFailure(
                    ERR_BOX_LIMIT,
                    f"more than {self.limits.max_boxes} boxes",
                    pos,
                )
            box = Box(
                type=_tname(btype),
                offset=pos,
                size=size,
                header_size=header,
                payload_offset=payload_offset,
                payload_size=payload_size,
                depth=depth,
            )
            if btype in CONTAINERS and payload_size:
                box.children = self._parse_range(
                    payload_offset, payload_offset + payload_size, depth + 1
                )
            boxes.append(box)
            pos += size
        return boxes


def _tname(raw: bytes) -> str:
    return raw.decode("latin-1")


def read_fullbox_header(fh: BinaryIO, box: Box) -> tuple[int, int, int]:
    """Return (version, flags, offset_after_fullbox_header)."""
    if box.payload_size < 4:
        raise ScanFailure(
            ERR_TRUNCATED,
            f"{box.type} FullBox header is truncated",
            box.payload_offset,
        )
    data = read_exact(fh, 4, box.payload_offset)
    return data[0], int.from_bytes(data[1:4], "big"), box.payload_offset + 4


def read_payload(fh: BinaryIO, box: Box, limit: int | None = None) -> bytes:
    """Read (at most ``limit``) bytes of a box payload, bounded by limits."""
    n = box.payload_size if limit is None else min(box.payload_size, limit)
    return read_exact(fh, n, box.payload_offset)


def parse_file(path: str, limits: Limits | None = None) -> tuple[list[Box], int]:
    """Parse a BMFF file from disk and return (top boxes, file size)."""
    limits = limits or Limits()
    try:
        size = os.path.getsize(path)
    except OSError as exc:
        raise ScanFailure(ERR_IO, f"cannot stat file: {exc}", 0) from exc
    limits.check_file_size(size)
    try:
        with open(path, "rb") as fh:
            boxes = BoxReader(fh, size, limits).parse()
    except OSError as exc:
        raise ScanFailure(ERR_IO, f"cannot read file: {exc}", 0) from exc
    return boxes, size
