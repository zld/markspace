"""Sample table (stbl) leaf boxes: stts, stsc, stsz, stco/co64.

Entry counts are capped so a hostile length field cannot trigger a
multi-gigabyte allocation; payload lengths must match the declared
entry count exactly.
"""
from __future__ import annotations

import struct
from dataclasses import dataclass

from .boxes import StructuralError

MAX_TABLE_ENTRIES = 5_000_000


class StblError(StructuralError):
    """Malformed sample table box (CLI exit code 2)."""


@dataclass
class Stts:
    entries: list  # of (sample_count, sample_delta)

    @property
    def sample_count(self):
        return sum(c for c, _ in self.entries)

    @property
    def total_delta(self):
        return sum(c * d for c, d in self.entries)


@dataclass
class Stsc:
    entries: list  # of (first_chunk, samples_per_chunk, sample_description_index)


@dataclass
class Stsz:
    sample_size: int      # non-zero => constant size, no per-sample entries
    sample_count: int
    entries: list         # per-sample sizes, empty when sample_size != 0

    def size_of(self, index):
        return self.sample_size if self.sample_size else self.entries[index]


@dataclass
class ChunkOffsets:
    offsets: list
    wide: bool            # True when read from co64


def _body(data, box):
    payload = data[box.data_offset:box.end]
    if len(payload) < 4:
        raise StblError(f"{box.type} at offset {box.offset}: truncated full-box header")
    return payload[4:]


def _check_count(box, count, entry_size, body_len):
    if count > MAX_TABLE_ENTRIES:
        raise StblError(
            f"{box.type} at offset {box.offset}: entry count {count} "
            f"exceeds limit {MAX_TABLE_ENTRIES}")
    if body_len != count * entry_size:
        raise StblError(
            f"{box.type} at offset {box.offset}: payload holds {body_len} "
            f"bytes, expected {count * entry_size} for {count} entries")


def parse_stts(data, box):
    body = _body(data, box)
    if len(body) < 4:
        raise StblError(f"stts at offset {box.offset}: missing entry count")
    (count,) = struct.unpack_from(">I", body, 0)
    _check_count(box, count, 8, len(body) - 4)
    entries = [struct.unpack_from(">II", body, 4 + i * 8) for i in range(count)]
    return Stts(entries)


def parse_stsc(data, box):
    body = _body(data, box)
    if len(body) < 4:
        raise StblError(f"stsc at offset {box.offset}: missing entry count")
    (count,) = struct.unpack_from(">I", body, 0)
    _check_count(box, count, 12, len(body) - 4)
    entries = [struct.unpack_from(">III", body, 4 + i * 12) for i in range(count)]
    return Stsc(entries)


def parse_stsz(data, box):
    body = _body(data, box)
    if len(body) < 8:
        raise StblError(f"stsz at offset {box.offset}: truncated header")
    sample_size, sample_count = struct.unpack_from(">II", body, 0)
    if sample_count > MAX_TABLE_ENTRIES:
        raise StblError(
            f"stsz at offset {box.offset}: sample count {sample_count} "
            f"exceeds limit {MAX_TABLE_ENTRIES}")
    entries = []
    if sample_size == 0:
        _check_count(box, sample_count, 4, len(body) - 8)
        entries = [struct.unpack_from(">I", body, 8 + i * 4)[0]
                   for i in range(sample_count)]
    elif len(body) != 8:
        raise StblError(
            f"stsz at offset {box.offset}: constant-size table carries "
            f"{len(body) - 8} stray bytes")
    return Stsz(sample_size, sample_count, entries)


def parse_stco(data, box):
    """Parse stco (32-bit) or co64 (64-bit) chunk offset tables."""
    body = _body(data, box)
    if len(body) < 4:
        raise StblError(f"{box.type} at offset {box.offset}: missing entry count")
    (count,) = struct.unpack_from(">I", body, 0)
    wide = box.type == "co64"
    entry_size = 8 if wide else 4
    _check_count(box, count, entry_size, len(body) - 4)
    fmt = ">Q" if wide else ">I"
    offsets = [struct.unpack_from(fmt, body, 4 + i * entry_size)[0]
               for i in range(count)]
    return ChunkOffsets(offsets, wide)


def samples_per_chunk(stsc_entries, chunk_count):
    """Expand the stsc run-length mapping to a per-chunk sample count list."""
    if chunk_count == 0:
        if stsc_entries:
            raise StblError("stsc declares entries but there are no chunks")
        return []
    if not stsc_entries:
        raise StblError("stsc is empty but chunks exist")
    if stsc_entries[0][0] != 1:
        raise StblError(
            f"stsc first entry starts at chunk {stsc_entries[0][0]}, expected 1")
    result = []
    for i, (first_chunk, per_chunk, _desc) in enumerate(stsc_entries):
        nxt = stsc_entries[i + 1][0] if i + 1 < len(stsc_entries) else chunk_count + 1
        if nxt <= first_chunk:
            raise StblError(
                f"stsc first_chunk values not strictly increasing at entry {i}")
        result.extend([per_chunk] * (nxt - first_chunk))
    if len(result) != chunk_count:
        raise StblError(
            f"stsc maps {len(result)} chunks but {chunk_count} chunk offsets exist")
    return result
