"""High-level fixture assembly: valid reference file and corrupted variants."""

from __future__ import annotations

from .boxes import find_deep, find_first
from ._fixture_builders import (
    CHUNK_PLAN,
    CTTS_OFFSETS,
    SAMPLE_DELTA,
    SAMPLE_SIZES,
    SEGMENT_PLAN,
    SYNC_SAMPLES,
    TIMESCALE,
    box,
    co64_box,
    container,
    ctts_box,
    ftyp_box,
    fullbox,
    i32,
    mdat_box,
    media_chunk,
    stco_box,
    stsc_box,
    stss_box,
    stsz_box,
    stts_box,
    u16,
    u32,
    u64,
)

# Offsets inside an sidx version-1 payload (payload starts after box header).
# Layout: fullbox(4) ref_id(4) timescale(4) ept(8) first_offset(8) reserved(2) count(2)
_SIDX_TIMESCALE = 4 + 4
_SIDX_V1_FIRST_OFFSET = 4 + 4 + 4 + 8
_SIDX_V1_ENTRIES = _SIDX_V1_FIRST_OFFSET + 8 + 2 + 2


def sidx_box(
    reference_id: int,
    timescale: int,
    earliest: int,
    first_offset: int,
    refs: list[tuple[int, int, int, bool]],
    version: int = 1,
) -> bytes:
    """Build an sidx box.

    refs tuples: (reference_type, referenced_size, subsegment_duration, sap).
    """

    body = u32(reference_id) + u32(timescale)
    if version == 0:
        body += u32(earliest) + u32(first_offset)
    else:
        body += u64(earliest) + u64(first_offset)
    body += u16(0) + u16(len(refs))
    for ref_type, size, duration, sap in refs:
        first_word = (ref_type << 31) | (size & 0x7FFFFFFF)
        sap_word = (1 << 31) if sap else 0
        body += u32(first_word) + u32(duration) + u32(sap_word)
    return fullbox("sidx", version, 0, body)


def _stbl_box(
    sizes: tuple[int, ...],
    chunk_offsets: tuple[int, ...],
    chunk_plan: tuple[int, ...],
    use_co64: bool = False,
    include_ctts: bool = True,
    include_stss: bool = True,
) -> bytes:
    children = [
        stsz_box(sizes),
        stts_box(SAMPLE_DELTA, len(sizes)),
        stsc_box(chunk_plan),
        co64_box(chunk_offsets) if use_co64 else stco_box(chunk_offsets),
    ]
    if include_ctts and sizes:
        children.append(ctts_box(CTTS_OFFSETS[: len(sizes)]))
    if include_stss and sizes:
        children.append(stss_box(SYNC_SAMPLES))
    return container("stbl", children)


def _moov_box(stbl: bytes) -> bytes:
    minf = container("minf", [stbl])
    mdia = container("mdia", [minf])
    trak = container("trak", [mdia])
    return container("moov", [trak])


def build_valid(use_co64: bool = False) -> bytes:
    """Build the deterministic valid reference MP4 (6 samples, 3 segments)."""

    chunk_sizes = _chunk_sizes(SAMPLE_SIZES, CHUNK_PLAN)
    # First pass: offset values do not change any box size.
    placeholder = tuple(0 for _ in chunk_sizes)
    moov = _moov_box(_stbl_box(SAMPLE_SIZES, placeholder, CHUNK_PLAN, use_co64))
    refs = [
        (0, chunk_sizes[i], SAMPLE_DELTA * SEGMENT_PLAN[i], i == 0)
        for i in range(len(SEGMENT_PLAN))
    ]
    sidx = sidx_box(1, TIMESCALE, 0, 8, refs, version=1)
    mdat_start = len(ftyp_box()) + len(moov) + len(sidx)
    chunk_offsets = _layout_offsets(mdat_start + 8, chunk_sizes)
    moov = _moov_box(
        _stbl_box(SAMPLE_SIZES, chunk_offsets, CHUNK_PLAN, use_co64)
    )
    chunks = [
        media_chunk(size, seed=idx) for idx, size in enumerate(chunk_sizes)
    ]
    return ftyp_box() + moov + sidx + mdat_box(chunks)


def build_nested() -> bytes:
    """Top sidx -> nested index sidx -> three media references."""

    chunk_sizes = _chunk_sizes(SAMPLE_SIZES, CHUNK_PLAN)
    placeholder = tuple(0 for _ in chunk_sizes)
    moov = _moov_box(_stbl_box(SAMPLE_SIZES, placeholder, CHUNK_PLAN))
    media_refs = [
        (0, chunk_sizes[i], SAMPLE_DELTA * SEGMENT_PLAN[i], i == 0)
        for i in range(len(SEGMENT_PLAN))
    ]
    # Nested sidx anchors at its own end; +8 skips the mdat header.
    nested = sidx_box(2, TIMESCALE, 0, 8, media_refs, version=0)
    top = sidx_box(
        1, TIMESCALE, 0, 0,
        [(1, len(nested), 0, False)], version=1,
    )
    mdat_start = len(ftyp_box()) + len(moov) + len(top) + len(nested)
    chunk_offsets = _layout_offsets(mdat_start + 8, chunk_sizes)
    moov = _moov_box(_stbl_box(SAMPLE_SIZES, chunk_offsets, CHUNK_PLAN))
    chunks = [media_chunk(size, seed=idx) for idx, size in enumerate(chunk_sizes)]
    return ftyp_box() + moov + top + nested + mdat_box(chunks)


def build_empty() -> bytes:
    """A parseable file with empty sample tables and no references."""

    moov = _moov_box(
        _stbl_box((), (), (), include_ctts=False, include_stss=False)
    )
    sidx = sidx_box(1, TIMESCALE, 0, 0, [], version=0)
    return ftyp_box() + moov + sidx + box("mdat", b"")


def _chunk_sizes(sizes: tuple[int, ...], plan: tuple[int, ...]) -> tuple[int, ...]:
    out: list[int] = []
    cursor = 0
    for count in plan:
        out.append(sum(sizes[cursor : cursor + count]))
        cursor += count
    assert cursor == len(sizes)
    return tuple(out)


def _layout_offsets(first: int, chunk_sizes: tuple[int, ...]) -> tuple[int, ...]:
    offsets: list[int] = []
    cursor = first
    for size in chunk_sizes:
        offsets.append(cursor)
        cursor += size
    return tuple(offsets)


# ---------------------------------------------------------------------------
# Corruption injectors. Each returns a new bytearray; none allocate beyond the
# input size, so malformed fixtures stay small.
# ---------------------------------------------------------------------------


def _box_position(data: bytes | bytearray, box_type: str, deep: bool) -> int:
    finder = find_deep if deep else find_first
    found = finder(memoryview(data), box_type)
    if found is None:
        raise KeyError(f"box {box_type} not found")
    return found[0].content_start


def _top_level_position(data: bytes | bytearray, box_type: str) -> int:
    return _box_position(data, box_type, deep=False)


def _deep_position(data: bytes | bytearray, box_type: str) -> int:
    return _box_position(data, box_type, deep=True)


def inject_misaligned(data: bytes, delta: int = 13) -> bytearray:
    """Shift sidx first_offset so references no longer align with chunks."""

    out = bytearray(data)
    payload = _top_level_position(out, "sidx")
    pos = payload + _SIDX_V1_FIRST_OFFSET
    value = int.from_bytes(out[pos : pos + 8], "big")
    out[pos : pos + 8] = (value + delta).to_bytes(8, "big")
    return out


def inject_oversized_reference(data: bytes, extra: int = 4096) -> bytearray:
    """Inflate the first sidx reference beyond end of file."""

    out = bytearray(data)
    payload = _top_level_position(out, "sidx")
    pos = payload + _SIDX_V1_ENTRIES
    word = int.from_bytes(out[pos : pos + 4], "big")
    size = (word & 0x7FFFFFFF) + extra
    out[pos : pos + 4] = ((word >> 31) << 31 | size).to_bytes(4, "big")
    return out


def inject_duplicate_chunk_offset(data: bytes) -> bytearray:
    """Force the second stco entry to equal the first one."""

    out = bytearray(data)
    payload = _deep_position(out, "stco")
    first = int.from_bytes(out[payload + 8 : payload + 12], "big")
    out[payload + 12 : payload + 16] = first.to_bytes(4, "big")
    return out


def inject_truncated_field(data: bytes) -> bytearray:
    """Chop the file inside the sidx entry area."""

    sidx_payload = _top_level_position(data, "sidx")
    return bytearray(data[: sidx_payload + _SIDX_V1_ENTRIES + 6])


def inject_truncated_header(data: bytes) -> bytearray:
    """Leave three trailing bytes that cannot form an 8-byte box header."""

    # Drop everything from the final mdat box header onward, then append a
    # three-byte fragment so the trailing bytes fail header parsing.
    mdat = find_first(memoryview(data), "mdat")
    if mdat is None:
        return bytearray(b"\x00\x00\x00")
    cut = mdat[0].header_start
    return bytearray(data[:cut]) + b"\x00\x00\x00"


def inject_bad_timescale(data: bytes) -> bytearray:
    """Zero the sidx timescale field (payload offset 8 in every version)."""

    out = bytearray(data)
    payload = _top_level_position(out, "sidx")
    out[payload + _SIDX_TIMESCALE : payload + _SIDX_TIMESCALE + 4] = (
        0).to_bytes(4, "big")
    return out


def inject_cycle(data: bytes) -> bytearray:
    """Redirect a nested-index fixture's index reference to its own sidx."""

    out = bytearray(data)
    found = find_first(memoryview(out), "sidx")
    assert found is not None
    sidx_header = found[0]
    payload = sidx_header.content_start
    pos = payload + _SIDX_V1_FIRST_OFFSET
    # first_offset becomes negative-equivalent: point back at the sidx header.
    target = sidx_header.header_start - sidx_header.end
    out[pos : pos + 8] = (target & ((1 << 64) - 1)).to_bytes(8, "big", signed=False)
    return out
