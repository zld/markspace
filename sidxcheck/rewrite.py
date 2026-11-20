"""Rewrite a classic MP4 with a fresh SIDX, patching chunk offsets.

The output is planned as an ordered list of pieces (copy / new SIDX / patched
moov), then streamed into a temporary file in the destination directory and
atomically moved into place. Growing or shrinking the SIDX simply shifts the
following bytes, so chunk-offset tables are repatched until the layout is
stable.
"""

from __future__ import annotations

import os
import struct
import tempfile
from dataclasses import dataclass, field
from pathlib import Path
from typing import BinaryIO

from .boxes import Box, BoxTree, parse_boxes, read_payload
from .errors import IOErrorFailure, MediaStructureError, UnsupportedFeatureError
from .moovpatch import MoovPatch, patch_moov
from .samplemap import SampleMap, build_sample_map
from .segments import SegmentPlan, plan_segments
from .sidx import Sidx, SidxReference, serialize_sidx
from .tables import load_track_tables, mdat_ranges

COPY_BLOCK = 1024 * 1024
MAX_LAYOUT_PASSES = 16


@dataclass
class RewriteResult:
    source: str
    destination: str
    source_size: int
    destination_size: int
    new_sidx_offset: int
    new_sidx_size: int
    index_version: int
    chunk_offset_kind: str
    samples: int
    segments: int
    layout_passes: int
    offset_shift: int
    warnings: list[str] = field(default_factory=list)


@dataclass
class _Piece:
    kind: str  # copy | sidx | moov
    size: int
    box: Box | None = None
    start: int = 0


@dataclass
class _Layout:
    pieces: list[_Piece]
    sidx_offset: int
    sidx_size: int
    mdat_payload_start: int
    shift: int
    first_offset: int
    patched: MoovPatch
    passes: int
    chunk_offset_kind: str


def _moov_size(payload_len: int) -> int:
    return 8 + payload_len if 8 + payload_len <= 0xFFFFFFFF else 16 + payload_len


def _plan(
    tree: BoxTree,
    *,
    existing: Box | None,
    first_mdat: Box,
    sidx_size: int,
    moov_len: int,
) -> list[_Piece]:
    pieces: list[_Piece] = []
    sidx_placed = False
    for box in tree.top_level:
        if existing is not None and box is existing:
            continue
        if box is first_mdat and not sidx_placed:
            pieces.append(_Piece("sidx", sidx_size))
            sidx_placed = True
        if box is first_mdat:
            pieces.append(_Piece("copy", box.size, box=box))
            continue
        if box.type == "moov":
            pieces.append(_Piece("moov", _moov_size(moov_len), box=box))
            continue
        pieces.append(_Piece("copy", box.size, box=box))
    if not sidx_placed:
        raise MediaStructureError("layout has no first mdat to anchor the SIDX", location="mdat")
    position = 0
    for piece in pieces:
        piece.start = position
        position += piece.size
    return pieces


def _piece_offsets(pieces: list[_Piece], first_mdat: Box) -> tuple[int, int]:
    sidx_offset = -1
    mdat_payload_start = -1
    for piece in pieces:
        if piece.kind == "sidx":
            sidx_offset = piece.start
        elif piece.kind == "copy" and piece.box is first_mdat:
            mdat_payload_start = piece.start + first_mdat.header_size
    if sidx_offset < 0 or mdat_payload_start < 0:
        raise MediaStructureError("layout lost its SIDX or mdat anchor", location="mdat")
    return sidx_offset, mdat_payload_start


def _layout(
    tree: BoxTree,
    sample_map: SampleMap,
    plan: SegmentPlan,
    *,
    existing: Box | None,
    moov: Box,
    moov_payload: bytes,
    first_mdat: Box,
) -> _Layout:
    if existing is not None and existing.start > first_mdat.start:
        raise UnsupportedFeatureError(
            "existing SIDX sits after the first mdat; references cannot be anchored "
            "without negative offsets",
            location="sidx",
        )
    if len(plan.segments) > 0xFFFF:
        raise UnsupportedFeatureError(
            f"{len(plan.segments)} references exceed the 65535 limit of one SIDX",
            location="sidx",
        )
    probe_refs = [
        SidxReference(0, seg.byte_size, seg.duration_ticks, 1, 1, 0) for seg in plan.segments
    ]
    if not probe_refs:
        raise MediaStructureError("segment plan produced no references", location="sidx")
    sidx_size = len(
        serialize_sidx(Sidx(0, sample_map.track_id, sample_map.timescale, 0, 0, list(probe_refs)))
    )
    moov_len = len(moov_payload)
    patched: MoovPatch | None = None
    shift = 0
    passes = 0
    pieces = _plan(
        tree,
        existing=existing,
        first_mdat=first_mdat,
        sidx_size=sidx_size,
        moov_len=moov_len,
    )
    while True:
        passes += 1
        if passes > MAX_LAYOUT_PASSES:
            raise MediaStructureError(
                f"layout did not stabilize after {MAX_LAYOUT_PASSES} passes",
                location="moov",
            )
        sidx_offset, mdat_payload_start = _piece_offsets(pieces, first_mdat)
        first_offset = mdat_payload_start - (sidx_offset + sidx_size)
        if first_offset < 0:
            raise UnsupportedFeatureError(
                f"new SIDX needs {sidx_size} bytes and would start after the first "
                f"mdat payload (first_offset {first_offset})",
                location="sidx",
            )
        if first_offset > 0xFFFFFFFF:
            widened = len(
                serialize_sidx(
                    Sidx(
                        1,
                        sample_map.track_id,
                        sample_map.timescale,
                        0,
                        first_offset,
                        list(probe_refs),
                    )
                )
            )
            if widened != sidx_size:
                sidx_size = widened
                pieces = _plan(
                    tree,
                    existing=existing,
                    first_mdat=first_mdat,
                    sidx_size=sidx_size,
                    moov_len=moov_len,
                )
                continue
            raise MediaStructureError(
                "first_offset does not fit the planned SIDX version", location="sidx"
            )
        new_shift = mdat_payload_start - first_mdat.payload_start
        patched = patch_moov(moov_payload, lambda _value, s=new_shift: s, where=moov.path)
        new_len = len(patched.data)
        if new_len != moov_len:
            moov_len = new_len
            pieces = _plan(
                tree,
                existing=existing,
                first_mdat=first_mdat,
                sidx_size=sidx_size,
                moov_len=moov_len,
            )
            continue
        shift = new_shift
        break
    assert patched is not None
    if not patched.patched_entries:
        raise MediaStructureError(
            "no chunk offset entries were patched; the track has no stco/co64",
            location="stbl",
        )
    return _Layout(
        pieces=pieces,
        sidx_offset=sidx_offset,
        sidx_size=sidx_size,
        mdat_payload_start=mdat_payload_start,
        shift=shift,
        first_offset=first_offset,
        patched=patched,
        passes=passes,
        chunk_offset_kind="co64" if tree.all("co64") else "stco",
    )


def _build_sidx(sample_map: SampleMap, plan: SegmentPlan, layout: _Layout) -> Sidx:
    references = [
        SidxReference(
            reference_type=0,
            referenced_size=segment.byte_size,
            subsegment_duration=segment.duration_ticks,
            starts_with_sap=1,
            sap_type=segment.sap_type,
            sap_delta_time=segment.sap_delta_time,
        )
        for segment in plan.segments
    ]
    version = 1 if layout.first_offset > 0xFFFFFFFF else 0
    sidx = Sidx(
        version=version,
        reference_id=sample_map.track_id,
        timescale=sample_map.timescale,
        earliest_presentation_time=0,
        first_offset=layout.first_offset,
        references=references,
        box_start=layout.sidx_offset,
        box_end=layout.sidx_offset + layout.sidx_size,
    )
    if len(serialize_sidx(sidx)) != layout.sidx_size:
        raise MediaStructureError(
            "SIDX serialization size disagrees with its planned layout", location="sidx"
        )
    return sidx


def _stream_copy(src: BinaryIO, dst: BinaryIO, start: int, end: int) -> None:
    src.seek(start)
    remaining = end - start
    while remaining > 0:
        block = src.read(min(COPY_BLOCK, remaining))
        if not block:
            raise IOErrorFailure(
                f"source ended early while copying [{start}, {end})", location="read"
            )
        dst.write(block)
        remaining -= len(block)


def _write_new_file(
    source: Path,
    destination: Path,
    *,
    layout: _Layout,
    sidx_bytes: bytes,
    moov_payload: bytes,
) -> None:
    destination.parent.mkdir(parents=True, exist_ok=True)
    fd, temp_name = tempfile.mkstemp(
        prefix=".sidxcheck-", suffix=".tmp", dir=str(destination.parent)
    )
    temp_path = Path(temp_name)
    try:
        with source.open("rb") as src, os.fdopen(fd, "wb") as dst:
            for piece in layout.pieces:
                if piece.kind == "sidx":
                    dst.write(sidx_bytes)
                elif piece.kind == "moov":
                    if _moov_size(len(moov_payload)) - len(moov_payload) == 8:
                        dst.write(struct.pack(">I4s", 8 + len(moov_payload), b"moov"))
                    else:
                        dst.write(struct.pack(">I4sQ", 1, b"moov", 16 + len(moov_payload)))
                    dst.write(moov_payload)
                else:
                    assert piece.box is not None
                    _stream_copy(src, dst, piece.box.start, piece.box.end)
            dst.flush()
            os.fsync(dst.fileno())
        os.replace(temp_path, destination)
    except BaseException:
        temp_path.unlink(missing_ok=True)
        raise


def rewrite_with_sidx(
    source: Path,
    destination: Path,
    *,
    track_id: int,
    segment_ticks: int,
    max_drift_ticks: int = 0,
) -> RewriteResult:
    if track_id <= 0:
        raise ValueError("track id must be positive")
    if segment_ticks <= 0:
        raise ValueError("segment ticks must be positive")
    try:
        source_size = source.stat().st_size
    except OSError as exc:
        raise IOErrorFailure(f"cannot stat {source}: {exc}", location=str(source)) from exc
    if source.resolve() == destination.resolve():
        raise IOErrorFailure(
            "rewrite destination must differ from the source file", location=str(source)
        )
    try:
        with source.open("rb") as fp:
            tree = parse_boxes(fp, source_size)
            if len(tree.all("mdat")) > 1:
                raise UnsupportedFeatureError(
                    f"file has {len(tree.all('mdat'))} mdat boxes; a single contiguous "
                    f"index requires exactly one",
                    location="mdat",
                )
            tables = load_track_tables(fp, tree, track_id)
            sample_map = build_sample_map(tables, mdat_ranges=mdat_ranges(tree))
            plan = plan_segments(
                sample_map,
                target_ticks=segment_ticks,
                max_drift_ticks=max_drift_ticks,
            )
            moov = tree.first("moov", required=True, unique=True)
            assert moov is not None
            top_sidx = [box for box in tree.all("sidx") if box.depth == 0]
            if len(top_sidx) > 1:
                raise UnsupportedFeatureError(
                    f"file has {len(top_sidx)} top-level SIDX boxes", location="sidx"
                )
            existing = top_sidx[0] if top_sidx else None
            first_mdat = tree.all("mdat")[0]
            moov_payload = read_payload(fp, moov)
            layout = _layout(
                tree,
                sample_map,
                plan,
                existing=existing,
                moov=moov,
                moov_payload=moov_payload,
                first_mdat=first_mdat,
            )
            sidx = _build_sidx(sample_map, plan, layout)
            sidx_bytes = serialize_sidx(sidx)
            warnings = list(sample_map.warnings) + list(plan.warnings)
            warnings.extend(layout.patched.warnings or [])
            _write_new_file(
                source,
                destination,
                layout=layout,
                sidx_bytes=sidx_bytes,
                moov_payload=layout.patched.data,
            )
    except OSError as exc:
        raise IOErrorFailure(f"cannot read {source}: {exc}", location=str(source)) from exc
    try:
        destination_size = destination.stat().st_size
    except OSError as exc:
        raise IOErrorFailure(
            f"rewritten file {destination} is missing: {exc}", location=str(destination)
        ) from exc
    expected = tree.file_size + layout.shift
    if destination_size != expected:
        raise IOErrorFailure(
            f"written size {destination_size} does not match planned {expected}",
            location=str(destination),
        )
    return RewriteResult(
        source=str(source),
        destination=str(destination),
        source_size=source_size,
        destination_size=destination_size,
        new_sidx_offset=layout.sidx_offset,
        new_sidx_size=layout.sidx_size,
        index_version=sidx.version,
        chunk_offset_kind=layout.chunk_offset_kind,
        samples=sample_map.sample_count,
        segments=len(plan.segments),
        layout_passes=layout.passes,
        offset_shift=layout.shift,
        warnings=warnings,
    )
