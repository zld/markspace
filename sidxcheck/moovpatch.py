"""Patch chunk offsets inside a moov tree, upgrading stco to co64 when needed."""

from __future__ import annotations

import struct
from dataclasses import dataclass
from typing import Callable

from .errors import MediaStructureError

PURE_CONTAINERS = frozenset({b"moov", b"trak", b"mdia", b"minf", b"stbl", b"edts", b"dinf"})
MAX_DEPTH = 16
STCO_MAX = (1 << 32) - 1
CO64_MAX = (1 << 64) - 1


@dataclass
class MoovPatch:
    data: bytes  # payload of the enclosing moov box
    upgrades: int = 0
    patched_entries: int = 0
    warnings: list[str] | None = None


def _split_children(data: bytes, where: str) -> list[tuple[bytes, bytes]]:
    """Return (type, raw_box) pairs for a pure container payload."""
    out: list[tuple[bytes, bytes]] = []
    position = 0
    length = len(data)
    while position < length:
        if length - position < 8:
            raise MediaStructureError(
                f"{where} has {length - position} trailing byte(s) that cannot hold a box header",
                location=where,
            )
        size32 = struct.unpack(">I", data[position : position + 4])[0]
        raw_type = data[position + 4 : position + 8]
        header = 8
        if size32 == 1:
            if length - position < 16:
                raise MediaStructureError(
                    f"{where} has a truncated 64-bit box header", location=where
                )
            size = struct.unpack(">Q", data[position + 8 : position + 16])[0]
            header = 16
        elif size32 == 0:
            size = length - position
        else:
            size = size32
        if size < header or position + size > length:
            raise MediaStructureError(
                f"{where} contains a box with invalid size {size}", location=where
            )
        out.append((raw_type, data[position : position + size]))
        position += size
    return out


def _pack_box(raw_type: bytes, payload: bytes) -> bytes:
    total = 8 + len(payload)
    if total <= 0xFFFFFFFF:
        return struct.pack(">I", total) + raw_type + payload
    return struct.pack(">I4sQ", 1, raw_type, 16 + len(payload)) + payload


def _payload_of(raw_box: bytes) -> tuple[int, bytes]:
    size32 = struct.unpack(">I", raw_box[:4])[0]
    header = 16 if size32 == 1 else 8
    return header, raw_box[header:]


def _patch_offset_table(
    raw_type: bytes,
    payload: bytes,
    shift_for_offset: Callable[[int], int],
    where: str,
) -> tuple[bytes, bool, int, list[str]]:
    """Return (new_payload, upgraded, patched_count, warnings)."""
    if len(payload) < 8:
        raise MediaStructureError(f"{where} is too short for a full box", location=where)
    version = payload[0]
    flags = payload[1:4]
    if version != 0 or flags != b"\x00\x00\x00":
        raise MediaStructureError(
            f"{where} has unsupported version/flags {version}/{flags.hex()}", location=where
        )
    count = struct.unpack(">I", payload[4:8])[0]
    width = 4 if raw_type == b"stco" else 8
    needed = 8 + count * width
    if len(payload) != needed:
        raise MediaStructureError(
            f"{where} declares {count} entries needing {needed} payload bytes, but has "
            f"{len(payload)}",
            location=where,
        )
    fmt = ">I" if width == 4 else ">Q"
    values: list[int] = []
    needs_upgrade = False
    for index in range(count):
        base = 8 + index * width
        (value,) = struct.unpack(fmt, payload[base : base + width])
        new_value = value + shift_for_offset(value)
        if new_value < 0:
            raise MediaStructureError(
                f"{where} entry {index} would become negative ({value} -> {new_value})",
                location=where,
            )
        if new_value > STCO_MAX and width == 4:
            needs_upgrade = True
        if new_value > CO64_MAX:
            raise MediaStructureError(
                f"{where} entry {index} exceeds 64-bit range", location=where
            )
        values.append(new_value)
    if needs_upgrade:
        body = struct.pack(">B3sI", 0, b"\x00\x00\x00", count) + b"".join(
            struct.pack(">Q", value) for value in values
        )
        return body, True, count, [
            f"{where} upgraded from stco to co64 because a shifted offset exceeded 2^32-1"
        ]
    body = struct.pack(">B3sI", 0, b"\x00\x00\x00", count) + b"".join(
        struct.pack(fmt, value) for value in values
    )
    return body, False, count, []


def _transform(
    raw_type: bytes,
    raw_box: bytes,
    shift_for_offset: Callable[[int], int],
    where: str,
    depth: int,
    state: dict[str, int],
    warnings: list[str],
) -> bytes:
    if depth > MAX_DEPTH:
        raise MediaStructureError(f"moov nesting exceeds {MAX_DEPTH}", location=where)
    header, payload = _payload_of(raw_box)
    if raw_type in (b"stco", b"co64"):
        new_payload, upgraded, patched, notes = _patch_offset_table(
            raw_type, payload, shift_for_offset, where
        )
        state["patched_entries"] += patched
        if upgraded:
            state["upgrades"] += 1
        warnings.extend(notes)
        return _pack_box(b"co64" if upgraded else raw_type, new_payload)
    if raw_type not in PURE_CONTAINERS:
        return raw_box  # untouched; header is already correct
    children = _split_children(payload, where)
    rebuilt = b"".join(
        _transform(
            child_type,
            child,
            shift_for_offset,
            f"{where}/{child_type.decode('latin-1')}",
            depth + 1,
            state,
            warnings,
        )
        for child_type, child in children
    )
    return _pack_box(raw_type, rebuilt)


def patch_moov(
    moov_payload: bytes,
    shift_for_offset: Callable[[int], int],
    *,
    where: str = "moov",
) -> MoovPatch:
    """Transform a moov *payload* and return new payload bytes plus statistics."""
    state = {"upgrades": 0, "patched_entries": 0}
    warnings: list[str] = []
    children = _split_children(moov_payload, where)
    rebuilt = b"".join(
        _transform(
            child_type,
            child,
            shift_for_offset,
            f"{where}/{child_type.decode('latin-1')}",
            1,
            state,
            warnings,
        )
        for child_type, child in children
    )
    return MoovPatch(
        data=rebuilt,
        upgrades=state["upgrades"],
        patched_entries=state["patched_entries"],
        warnings=warnings,
    )
