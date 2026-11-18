"""Sample table (index) reconstruction: stts, ctts, stsc, stsz, stco/co64, stss."""
from __future__ import annotations

MAX_TABLE_ENTRIES = 6_000_000
UINT32_MAX = 0xFFFFFFFF


class IndexBuildError(Exception):
    """Sample tables are inconsistent or exceed safety limits."""


def rle(values) -> list[list[int]]:
    """Run-length encode a sequence of ints as [count, value] pairs."""
    out: list[list[int]] = []
    for v in values:
        v = int(v)
        if out and out[-1][1] == v:
            out[-1][0] += 1
        else:
            out.append([1, v])
    return out


def build_stts(durations) -> list[list[int]]:
    """Decoding-time deltas; every entry must be a positive uint32."""
    entries = rle(durations)
    for _, delta in entries:
        if not 0 < delta <= UINT32_MAX:
            raise IndexBuildError(f"invalid stts delta {delta}")
    return entries


def build_ctts(composition_offsets) -> list[list[int]] | None:
    """Composition (pts-dts) offsets; None when all zero (table omitted)."""
    if not any(composition_offsets):
        return None
    return rle(composition_offsets)


def build_stss(keyframes) -> list[int]:
    """1-based sample numbers of sync samples."""
    return [i + 1 for i, k in enumerate(keyframes) if k]


def build_stsc() -> list[list[int]]:
    """One sample per chunk: a single [first_chunk, samples, desc] entry."""
    return [[1, 1, 1]]


def chunk_offsets(sizes, base: int) -> tuple[list[int], int]:
    """Absolute file offsets for one-sample chunks starting at ``base``."""
    offsets, pos = [], base
    for s in sizes:
        offsets.append(pos)
        pos += int(s)
    return offsets, pos


def needs_co64(offsets) -> bool:
    return any(o > UINT32_MAX for o in offsets)


def validate_tables(sample_count: int, stts, sizes, offsets,
                    end_offset: int, file_size: int) -> int:
    """Cross-check table counts and bounds; returns total entry count."""
    if sum(count for count, _ in stts) != sample_count:
        raise IndexBuildError("stts count does not match sample count")
    if len(sizes) != sample_count:
        raise IndexBuildError("stsz count does not match sample count")
    if len(offsets) != sample_count:
        raise IndexBuildError("stco count does not match chunk count")
    if end_offset > file_size:
        raise IndexBuildError("chunk offsets extend past end of file")
    entries = len(stts) + len(sizes) + len(offsets)
    if entries > MAX_TABLE_ENTRIES:
        raise IndexBuildError(f"index size {entries} exceeds limit")
    return entries
