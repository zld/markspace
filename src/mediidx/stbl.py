"""Parse the minimal ISO BMFF movie structure needed for sample layout.

Only one non-fragmented video track is supported.  The parser rejects
duplicate critical tables, unsupported encryption boxes, fragmented tracks and
sample tables whose counts disagree with the chunk / offset tables, always
naming the offending box and its absolute offset.
"""

from __future__ import annotations

from .box_reader import Box, find_all, find_first
from .cursor import Cursor
from .errors import ConsistencyError, StructureError
from .model import Track
from .sample_table import (
    assign_times,
    check_chunk_ranges,
    expand_chunks,
    samples_per_chunk,
)
from .tables import (
    MAX_CHUNKS,
    MAX_SAMPLES,
    cursor_for,
    parse_co64,
    parse_stco,
    parse_stsc,
    parse_stts,
    parse_stsz,
)

# Box types that would change the meaning of the sample data in a way this
# tool does not implement.  Encrypted / fragmented tracks must stop the run.
UNSUPPORTED_TRACK_BOXES = frozenset(
    {b"saiz", b"saio", b"senc", b"tenc", b"pssh", b"mvex", b"trex", b"moof", b"traf"}
)


def parse_offsets(view: memoryview, stbl: Box) -> tuple[bytes, list[int]]:
    stco = _optional_single_child(stbl, b"stco")
    co64 = _optional_single_child(stbl, b"co64")
    if stco is not None and co64 is not None:
        raise StructureError(
            "stbl contains both stco and co64",
            box_type=stbl.type,
            offset=stbl.start,
        )
    if stco is not None:
        return b"stco", parse_stco(view, stco)
    if co64 is not None:
        return b"co64", parse_co64(view, co64)
    raise StructureError(
        "required stco/co64 box is missing from stbl",
        box_type=stbl.type,
        offset=stbl.start,
    )



def _single_child(parent: Box, box_type: bytes) -> Box:
    matches = find_all(parent.children, box_type)
    if not matches:
        raise StructureError(
            f"required {box_type.decode('latin-1')} box is missing from "
            f"{parent.label}",
            box_type=parent.type,
            offset=parent.start,
        )
    if len(matches) > 1:
        raise StructureError(
            f"duplicate {box_type.decode('latin-1')} box in {parent.label}",
            box_type=box_type,
            offset=matches[1].start,
        )
    return matches[0]


def _optional_single_child(parent: Box, box_type: bytes) -> Box | None:
    matches = find_all(parent.children, box_type)
    if len(matches) > 1:
        raise StructureError(
            f"duplicate {box_type.decode('latin-1')} box in {parent.label}",
            box_type=box_type,
            offset=matches[1].start,
        )
    return matches[0] if matches else None


# --------------------------------------------------------------------------
# Movie / track traversal
# --------------------------------------------------------------------------


def _parse_mvhd_timescale(view: memoryview, moov: Box) -> int:
    mvhd = _single_child(moov, b"mvhd")
    cursor = cursor_for(view, mvhd)
    version = cursor.u8()
    cursor.skip(3)
    if version not in (0, 1):
        raise StructureError(
            f"unsupported mvhd version {version}", box_type=b"mvhd", offset=mvhd.start
        )
    if version == 0:
        cursor.skip(4 + 4)  # creation + modification
        timescale = cursor.u32()
    else:
        cursor.skip(8 + 8)
        timescale = cursor.u32()
    if timescale == 0:
        raise StructureError(
            "mvhd timescale is zero", box_type=b"mvhd", offset=mvhd.start
        )
    return timescale


def _parse_mdhd_timescale(view: memoryview, mdia: Box) -> int:
    mdhd = _single_child(mdia, b"mdhd")
    cursor = cursor_for(view, mdhd)
    version = cursor.u8()
    cursor.skip(3)
    if version not in (0, 1):
        raise StructureError(
            f"unsupported mdhd version {version}", box_type=b"mdhd", offset=mdhd.start
        )
    if version == 0:
        cursor.skip(4 + 4)
        timescale = cursor.u32()
    else:
        cursor.skip(8 + 8)
        timescale = cursor.u32()
    if timescale == 0:
        raise StructureError(
            "mdhd timescale is zero", box_type=b"mdhd", offset=mdhd.start
        )
    return timescale


def _parse_hdlr(view: memoryview, mdia: Box) -> bytes:
    hdlr = _single_child(mdia, b"hdlr")
    cursor = cursor_for(view, hdlr)
    cursor.skip(4)
    cursor.skip(4)  # pre_defined
    handler = bytes(cursor.take(4))
    return handler


def _reject_unsupported_stbl(stbl: Box) -> None:
    child_types = {child.type for child in stbl.children}
    for bad in sorted(child_types & UNSUPPORTED_TRACK_BOXES):
        bad_box = find_first(stbl.children, bad)
        raise StructureError(
            f"unsupported encrypted or fragmented box {bad.decode('latin-1')}",
            box_type=bad,
            offset=bad_box.start if bad_box else stbl.start,
        )


def find_mdat(view: memoryview, roots: list[Box]) -> Box:
    mdats = find_all(roots, b"mdat")
    if not mdats:
        raise StructureError("no mdat box found at the top level")
    if len(mdats) > 1:
        raise StructureError(
            f"expected exactly one mdat box, found {len(mdats)}",
            box_type=b"mdat",
            offset=mdats[1].start,
        )
    return mdats[0]


def parse_track(view: memoryview, roots: list[Box], mdat: Box) -> Track:
    moovs = find_all(roots, b"moov")
    if not moovs:
        raise StructureError("no moov box found at the top level")
    if len(moovs) > 1:
        raise StructureError(
            f"duplicate moov box", box_type=b"moov", offset=moovs[1].start
        )
    moov = moovs[0]
    _parse_mvhd_timescale(view, moov)  # validates presence / version

    traks = find_all(moov.children, b"trak")
    if len(traks) != 1:
        raise StructureError(
            f"expected exactly one trak box, found {len(traks)}",
            box_type=b"moov",
            offset=moov.start,
        )
    trak = traks[0]
    for bad in sorted(find_all(trak.children, b"traf") + find_all(trak.children, b"mvex")):
        raise StructureError(
            f"fragmented tracks are not supported ({bad.label})",
            box_type=bad.type,
            offset=bad.start,
        )

    mdia = _single_child(trak, b"mdia")
    minf = _single_child(mdia, b"minf")
    stbl = _single_child(minf, b"stbl")
    _reject_unsupported_stbl(stbl)

    handler = _parse_hdlr(view, mdia)
    if handler != b"vide":
        raise StructureError(
            f"unsupported track handler {handler.decode('latin-1', 'replace')!r}; "
            "only a single video track is accepted",
            box_type=b"hdlr",
            offset=mdia.start,
        )

    timescale = _parse_mdhd_timescale(view, mdia)

    stbl_start = stbl.start
    stts_box = _single_child(stbl, b"stts")
    stsc_box = _single_child(stbl, b"stsc")
    stsz_box = _single_child(stbl, b"stsz")
    if _optional_single_child(stbl, b"stz2") is not None:
        raise StructureError(
            "compact stz2 sample sizes are not supported; expected stsz",
            box_type=b"stz2",
            offset=find_first(stbl.children, b"stz2").start,
        )
    if _optional_single_child(stbl, b"ctts") is not None:
        # Composition offsets do not change decode-time grouping, but the task
        # only accepts stts/stsc/stsz with stco/co64.
        raise StructureError(
            "unsupported ctts composition offsets",
            box_type=b"ctts",
            offset=find_first(stbl.children, b"ctts").start,
        )

    stts = parse_stts(view, stts_box)
    stsc = parse_stsc(view, stsc_box)
    _uniform, sizes = parse_stsz(view, stsz_box)
    if len(sizes) > MAX_SAMPLES:
        raise StructureError(
            f"sample count {len(sizes)} exceeds limit {MAX_SAMPLES}",
            box_type=b"stsz",
            offset=stsz_box.start,
        )
    offsets_box, offsets = parse_offsets(view, stbl)
    if len(offsets) > MAX_CHUNKS:
        raise StructureError(
            f"chunk count {len(offsets)} exceeds limit {MAX_CHUNKS}",
            box_type=offsets_box,
            offset=stbl_start,
        )

    counts = samples_per_chunk(stsc, len(offsets), stsc_box.start)
    chunks = expand_chunks(offsets, counts, sizes, stsc_box)

    mdat_start = mdat.payload_start
    mdat_end = mdat.end
    check_chunk_ranges(chunks, mdat_start, mdat_end)

    flat: list[tuple[int, int, int]] = []
    for chunk in chunks:
        cursor = chunk.offset
        for sample_position in range(chunk.first_sample, chunk.sample_end):
            flat.append((cursor, sizes[sample_position], chunk.index))
            cursor += sizes[sample_position]
        if cursor != chunk.end:
            raise ConsistencyError(
                f"chunk {chunk.index} samples end at {cursor} but the chunk "
                f"is declared to end at {chunk.end}",
                box_type=b"stsc",
                offset=chunk.offset,
            )

    samples = assign_times(flat, stts, stts_box.start)
    return Track(
        track_id=_parse_tkhd_id(view, trak),
        timescale=timescale,
        handler=handler,
        samples=samples,
        chunks=chunks,
        mdat_start=mdat_start,
        mdat_end=mdat_end,
        offsets_box=offsets_box,
    )


def _parse_tkhd_id(view: memoryview, trak: Box) -> int:
    tkhd = _single_child(trak, b"tkhd")
    cursor = cursor_for(view, tkhd)
    version = cursor.u8()
    cursor.skip(3)  # flags
    if version == 0:
        cursor.skip(4 + 4)  # creation_time, modification_time
        return cursor.u32()
    if version == 1:
        cursor.skip(8 + 8)
        return cursor.u32()
    raise StructureError(
        f"unsupported tkhd version {version}", box_type=b"tkhd", offset=tkhd.start
    )
