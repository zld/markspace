"""Streaming ISO BMFF box header parsing and bounded payload reads.

Only boxes whose payload the analyser needs (sidx and the stbl leaf tables)
are buffered; mdat media payloads are skipped with ``seek`` so that a 1 GiB
file never costs more than a few KiB of memory.
"""

from __future__ import annotations

import struct
from collections.abc import Iterator
from dataclasses import dataclass

from . import model

HEADER_SIZE = 8
EXTENDED_HEADER_SIZE = 16
FULLBOX_SIZE = 4
# A payload bigger than this would blow the 128 MiB memory budget; entry-count
# tables inside such a box are untrusted regardless.
MAX_BUFFERED_PAYLOAD = 64 * 1024 * 1024
# Struct fields never exceed 64 bits and every validated box is much smaller.
MAX_BOX_SIZE = 1 << 62
MAX_BOX_DEPTH = 24

CONTAINER_TYPES = frozenset(
    {"moov", "trak", "mdia", "minf", "stbl", "mvex", "moof", "traf", "edts"}
)
# Types that hold leaf tables rather than nested boxes.
LEAF_TYPES = frozenset({"sidx", "stsz", "stsc", "stco", "co64", "stts"})


@dataclass(frozen=True)
class BoxHeader:
    box_type: str
    box_offset: int        # absolute file offset of the box header
    header_size: int       # 8 (size32) or 16 (largesize)
    content_start: int     # absolute offset of the first payload byte
    content_end: int       # absolute offset one past the last payload byte

    @property
    def content_size(self) -> int:
        return self.content_end - self.content_start


@dataclass(frozen=True)
class LocatedBox:
    header: BoxHeader
    payload: bytes  # only retained for small leaf boxes the tool actually parses


class _BoxScan:
    """Shared strict/lenient behaviour for structural problems."""

    def __init__(self, strict: bool) -> None:
        self.strict = strict
        self.diagnostics: list[model.Diagnostic] = []

    def fail(
        self,
        code: str,
        message: str,
        *,
        box_offset: int | None = None,
        box_type: str | None = None,
        entry_index: int | None = None,
        severity: str = model.SEV_ERROR,
    ) -> None:
        self.diagnostics.append(
            model.Diagnostic(
                severity, code, message, box_offset, box_type, entry_index
            )
        )
        if self.strict:
            raise model.CorruptContainerError(message)


def iter_box_headers(
    stream,
    start: int,
    end: int,
    scan: _BoxScan,
    *,
    depth: int = 0,
) -> Iterator[BoxHeader]:
    """Yield every well-formed box header inside ``[start, end)``.

    Structural failures are recorded on ``scan`` (and raised immediately in
    strict mode).  In lenient mode iteration stops at the first unparseable
    boundary because the next box position cannot be located safely.
    """
    if depth > MAX_BOX_DEPTH:
        scan.fail(
            model.BOX_NESTING_TOO_DEEP,
            f"box nesting exceeds {MAX_BOX_DEPTH} levels at offset {start}",
            box_offset=start,
        )
        return

    offset = start
    while offset < end:
        stream.seek(offset)
        head = stream.read(HEADER_SIZE)
        if len(head) < HEADER_SIZE:
            scan.fail(
                model.BOX_TRUNCATED_HEADER,
                f"truncated box header at offset {offset}: only "
                f"{len(head)} of {HEADER_SIZE} bytes readable",
                box_offset=offset,
            )
            return

        size32, box_type_raw = struct.unpack(">I4s", head)
        box_type = box_type_raw.decode("latin-1")
        header_size = HEADER_SIZE

        if size32 == 1:
            # 64-bit largesize follows the standard 8-byte header.  The value
            # 1 in size32 is an escape marker, not an invalid small length.
            ext = stream.read(8)
            if len(ext) < 8:
                scan.fail(
                    model.BOX_TRUNCATED_HEADER,
                    f"box {box_type!r} at offset {offset} promises a 64-bit "
                    "largesize but the extended header is truncated",
                    box_offset=offset,
                    box_type=box_type,
                )
                return
            (size,) = struct.unpack(">Q", ext)
            header_size = EXTENDED_HEADER_SIZE
            if size < EXTENDED_HEADER_SIZE:
                scan.fail(
                    model.BOX_SIZE_BELOW_HEADER,
                    f"box {box_type!r} at offset {offset} largesize {size} is "
                    f"smaller than its {EXTENDED_HEADER_SIZE}-byte header",
                    box_offset=offset,
                    box_type=box_type,
                )
                return
            box_size = size
        elif 0 < size32 < HEADER_SIZE:
            scan.fail(
                model.BOX_SIZE_BELOW_HEADER,
                f"box {box_type!r} at offset {offset} declares size {size32} "
                f"smaller than its {HEADER_SIZE}-byte header",
                box_offset=offset,
                box_type=box_type,
            )
            return
        elif size32 == 0:
            # Box extends to the end of the enclosing range / file.
            box_size = end - offset
        else:
            box_size = size32

        if box_size > MAX_BOX_SIZE:
            scan.fail(
                model.BOX_SIZE_OVERFLOW,
                f"box {box_type!r} at offset {offset} size {box_size} exceeds "
                f"the trusted limit of {MAX_BOX_SIZE} bytes",
                box_offset=offset,
                box_type=box_type,
            )
            return

        content_start = offset + header_size
        content_end = offset + box_size
        if content_end > end:
            scan.fail(
                model.BOX_BEYOND_FILE,
                f"box {box_type!r} at offset {offset} ends at {content_end}, "
                f"beyond the enclosing boundary {end}",
                box_offset=offset,
                box_type=box_type,
            )
            return

        yield BoxHeader(
            box_type=box_type,
            box_offset=offset,
            header_size=header_size,
            content_start=content_start,
            content_end=content_end,
        )
        offset = content_end


def read_payload(stream, header: BoxHeader, scan: _BoxScan) -> bytes | None:
    """Read a box payload, enforcing short-read and memory-budget checks."""
    size = header.content_size
    if size > MAX_BUFFERED_PAYLOAD:
        scan.fail(
            model.BOX_PAYLOAD_TOO_LARGE,
            f"box {header.box_type!r} at offset {header.box_offset} payload "
            f"{size} bytes exceeds the {MAX_BUFFERED_PAYLOAD} buffering limit",
            box_offset=header.box_offset,
            box_type=header.box_type,
        )
        return None
    stream.seek(header.content_start)
    payload = stream.read(size)
    if len(payload) != size:
        scan.fail(
            model.BOX_PAYLOAD_TRUNCATED,
            f"box {header.box_type!r} at offset {header.box_offset}: read "
            f"{len(payload)} of {size} payload bytes",
            box_offset=header.box_offset,
            box_type=header.box_type,
        )
        return None
    return payload


def read_fullbox_prefix(
    payload: bytes, header: BoxHeader, scan: _BoxScan
) -> tuple[int, int, int] | None:
    """Return (version, flags, payload_offset) or record a truncation error."""
    if len(payload) < FULLBOX_SIZE:
        scan.fail(
            model.FULLBOX_TRUNCATED,
            f"fullbox {header.box_type!r} at offset {header.box_offset} has no "
            "version/flags prefix",
            box_offset=header.box_offset,
            box_type=header.box_type,
        )
        return None
    version = payload[0]
    flags = int.from_bytes(payload[1:4], "big")
    return version, flags, FULLBOX_SIZE


def scan_stream(
    stream,
    file_size: int,
    target_types: frozenset[str],
    *,
    strict: bool,
) -> tuple[list[LocatedBox], list[model.Diagnostic]]:
    """Walk the box tree and buffer only ``target_types`` leaf boxes."""
    scan = _BoxScan(strict)
    found: list[LocatedBox] = []

    def walk(start: int, end: int, depth: int) -> None:
        for header in iter_box_headers(stream, start, end, scan, depth=depth):
            if header.box_type in target_types:
                payload = read_payload(stream, header, scan)
                if payload is not None:
                    found.append(LocatedBox(header, payload))
                continue
            if header.box_type in CONTAINER_TYPES and header.content_size > 0:
                walk(header.content_start, header.content_end, depth + 1)

    walk(0, file_size, 0)
    return found, scan.diagnostics
