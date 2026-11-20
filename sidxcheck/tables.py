"""Track discovery and stbl table loading for classic MP4 tracks."""

from __future__ import annotations

from typing import BinaryIO

from .boxes import Box, BoxTree, read_payload
from .errors import BoxLimitError, MediaStructureError, UnsupportedFeatureError
from .samples import (
    MAX_SAMPLES,
    CompositionOffsetEntry,
    SampleTables,
    SampleToChunkEntry,
    TimeToSampleEntry,
    parse_co64,
    parse_ctts,
    parse_hdlr,
    parse_mdhd,
    parse_stco,
    parse_stsc,
    parse_stsd,
    parse_stss,
    parse_stsz,
    parse_stts,
    parse_stz2,
    parse_tkhd,
)

ENCRYPTED_SAMPLE_ENTRIES = frozenset({"encv", "enca", "encs", "drmi", "drmv"})


def _single_child(tree: BoxTree, parent: Box, box_type: str, *, required: bool) -> Box | None:
    matches = [b for b in tree.children(parent) if b.type == box_type]
    if len(matches) > 1:
        raise MediaStructureError(
            f"{parent.path} contains {len(matches)} {box_type!r} boxes, expected one",
            location=parent.path,
        )
    if not matches:
        if required:
            raise MediaStructureError(
                f"{parent.path} is missing required {box_type!r} box", location=parent.path
            )
        return None
    return matches[0]


def list_track_ids(fp: BinaryIO, tree: BoxTree) -> list[int]:
    moov = tree.first("moov", required=True, unique=True)
    assert moov is not None
    track_ids: list[int] = []
    for trak in tree.children(moov):
        if trak.type != "trak":
            continue
        tkhd = _single_child(tree, trak, "tkhd", required=True)
        assert tkhd is not None
        track_ids.append(parse_tkhd(tkhd, read_payload(fp, tkhd)).track_id)
    return track_ids


def load_track_tables(fp: BinaryIO, tree: BoxTree, track_id: int) -> SampleTables:
    moov = tree.first("moov", required=True, unique=True)
    assert moov is not None
    matches: list[Box] = []
    for trak in tree.children(moov):
        if trak.type != "trak":
            continue
        tkhd = _single_child(tree, trak, "tkhd", required=True)
        assert tkhd is not None
        header = parse_tkhd(tkhd, read_payload(fp, tkhd))
        if header.track_id == track_id:
            matches.append(trak)
    if not matches:
        raise MediaStructureError(f"no trak with track_ID {track_id}", location="moov")
    if len(matches) > 1:
        raise MediaStructureError(
            f"{len(matches)} traks share track_ID {track_id}", location="moov"
        )
    trak = matches[0]
    mdia = _single_child(tree, trak, "mdia", required=True)
    assert mdia is not None
    mdhd = _single_child(tree, mdia, "mdhd", required=True)
    hdlr = _single_child(tree, mdia, "hdlr", required=True)
    minf = _single_child(tree, mdia, "minf", required=True)
    assert mdhd is not None and hdlr is not None and minf is not None
    stbl = _single_child(tree, minf, "stbl", required=True)
    assert stbl is not None
    if _single_child(tree, minf, "mvex", required=False) is not None:
        raise UnsupportedFeatureError("mvex inside minf is not supported", location=minf.path)
    timescale, duration, mdhd_version, language = parse_mdhd(mdhd, read_payload(fp, mdhd))
    handler_type = parse_hdlr(hdlr, read_payload(fp, hdlr))

    stts_box = _single_child(tree, stbl, "stts", required=True)
    stsc_box = _single_child(tree, stbl, "stsc", required=True)
    stsd_box = _single_child(tree, stbl, "stsd", required=True)
    assert stts_box is not None and stsc_box is not None and stsd_box is not None
    stsz_box = _single_child(tree, stbl, "stsz", required=False)
    stz2_box = _single_child(tree, stbl, "stz2", required=False)
    if (stsz_box is None) == (stz2_box is None):
        raise MediaStructureError(
            "stbl must contain exactly one of stsz or stz2", location=stbl.path
        )
    stco_box = _single_child(tree, stbl, "stco", required=False)
    co64_box = _single_child(tree, stbl, "co64", required=False)
    if (stco_box is None) == (co64_box is None):
        raise MediaStructureError(
            "stbl must contain exactly one of stco or co64", location=stbl.path
        )
    stss_box = _single_child(tree, stbl, "stss", required=False)
    ctts_box = _single_child(tree, stbl, "ctts", required=False)

    stts = parse_stts(stts_box, read_payload(fp, stts_box))
    ctts = parse_ctts(ctts_box, read_payload(fp, ctts_box)) if ctts_box else None
    stsc = parse_stsc(stsc_box, read_payload(fp, stsc_box))
    if stsz_box is not None:
        sample_sizes, stsz_kind = parse_stsz(stsz_box, read_payload(fp, stsz_box))
    else:
        assert stz2_box is not None
        sample_sizes, stsz_kind = parse_stz2(stz2_box, read_payload(fp, stz2_box))
    if stco_box is not None:
        chunk_offsets = parse_stco(stco_box, read_payload(fp, stco_box))
    else:
        assert co64_box is not None
        chunk_offsets = parse_co64(co64_box, read_payload(fp, co64_box))
    sync_samples = (
        parse_stss(stss_box, read_payload(fp, stss_box)) if stss_box is not None else None
    )
    description_count, entry_types = parse_stsd(stsd_box, read_payload(fp, stsd_box))
    for entry_type in entry_types:
        if entry_type in ENCRYPTED_SAMPLE_ENTRIES:
            raise UnsupportedFeatureError(
                f"stsd sample entry {entry_type!r} indicates encrypted samples",
                location=stsd_box.path,
            )
    if len(sample_sizes) > MAX_SAMPLES:
        raise BoxLimitError(
            f"track {track_id} has {len(sample_sizes)} samples, limit is {MAX_SAMPLES}",
            location=stbl.path,
        )
    return SampleTables(
        track_id=track_id,
        timescale=timescale,
        duration=duration,
        handler_type=handler_type,
        stts=stts,
        ctts=ctts,
        stsc=stsc,
        sample_sizes=sample_sizes,
        chunk_offsets=chunk_offsets,
        sync_samples=sync_samples,
        stsz_kind=stsz_kind,
        sample_description_count=description_count,
        language=language,
        mdhd_version=mdhd_version,
    )


def mdat_ranges(tree: BoxTree) -> list[tuple[int, int]]:
    boxes = tree.all("mdat")
    if not boxes:
        raise MediaStructureError("file has no mdat box", location="mdat")
    for box in boxes:
        if box.payload_size == 0:
            raise MediaStructureError("file has an empty mdat box", location=box.path)
    return [(box.payload_start, box.end) for box in boxes]
