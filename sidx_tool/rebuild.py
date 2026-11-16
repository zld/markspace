"""Rebuild media layout, chunk tables and SIDX, then atomically replace output.

Pipeline: parse source -> expand sample tables -> plan segments -> lay out
per-segment/per-track mdats -> render fresh moov (stsc + stco/co64) and SIDX ->
stream everything to a temp file -> independent re-read validation -> atomic
rename.  Any failure before the rename removes the temp file, so a broken run
can never leave a half-written target.
"""

from __future__ import annotations

import os
import tempfile
from dataclasses import dataclass
from pathlib import Path
from typing import BinaryIO

from . import IO_BUFFER
from .boxes import Box, parse_file
from .errors import FileError, FormatError, ValidationError
from .layout import Layout, collect_sample_bytes, plan_segments
from .moov_writer import MoovRewriter
from .sample_tables import SampleTable, parse_trak, validate_mdat_coverage
from .sidx import Sidx, build_sidx
from .validate import verify_bytes

MDAT_HEADER = 8


@dataclass
class RebuildResult:
    output: Path
    layout: Layout
    segment_ranges: list[tuple[int, int]]
    sidx: Sidx
    total_bytes: int


def _load(path: Path) -> tuple[list[Box], list[SampleTable], list[Box]]:
    top = parse_file(path)
    moov = next((b for b in top if b.type == "moov"), None)
    if moov is None:
        raise FormatError("input has no moov box")
    mdats = [b for b in top if b.type == "mdat"]
    if not mdats:
        raise FormatError("input has no mdat box")
    with path.open("rb") as stream:
        tables = [parse_trak(trak, stream) for trak in moov.find_all("trak")]
        _relocate_if_needed(tables, mdats)
        validate_mdat_coverage(tables, mdats)
    return top, tables, mdats


def _relocate_if_needed(tables: list[SampleTable], mdats: list[Box]) -> None:
    """Recover from a uniform mdat relocation that invalidated stco/co64.

    When recorded chunk offsets miss every mdat but one common delta moves all
    of them back inside, the media region was shifted (e.g. a header edit moved
    mdat without refreshing stco); apply the single compensating delta.
    Non-uniform or ambiguous failures stay hard errors, never silent guesses.
    """
    ranges = sorted((m.start, m.end) for m in mdats)

    def inside(offset: int, length: int) -> bool:
        return any(lo <= offset and offset + length <= hi for lo, hi in ranges)

    def all_resolve(delta: int) -> bool:
        for table in tables:
            for chunk in table.chunks:
                length = sum(table.sizes[i] for i in chunk.samples)
                if not inside(chunk.offset + delta, length):
                    return False
        return True

    if all_resolve(0):
        return
    # Candidate deltas: shift the first failing chunk onto each mdat start.
    first_bad = next(
        (table, chunk) for table in tables for chunk in table.chunks
        if not inside(chunk.offset, sum(table.sizes[i] for i in chunk.samples))
    )
    table0, chunk0 = first_bad
    candidates = {m.start - chunk0.offset for m in mdats}
    working = [delta for delta in candidates if all_resolve(delta)]
    if len(working) != 1:
        raise FormatError(
            "chunk offsets fall outside mdat and no unique relocation delta exists"
        )
    delta = working[0]
    for table in tables:
        for chunk in table.chunks:
            chunk.offset += delta
        table._sample_offsets = [offset + delta for offset in table._sample_offsets]


def _plan_chunks(layout: Layout, tables: list[SampleTable], mdat_offsets: dict[tuple[int, int], int]):
    """Return per-track chunk plan and per-segment mdat payload byte sizes."""
    plans: dict[int, list[tuple[int, int]]] = {t.track_id: [] for t in tables}
    payload_sizes: dict[int, int] = {}
    by_id = {t.track_id: t for t in tables}
    for segment in layout.segments:
        for track_id, (start, end) in segment.track_samples.items():
            table = by_id[track_id]
            length = sum(table.sizes[i] for i in range(start, end))
            payload_sizes[(segment.index, track_id)] = length
            plans[track_id].append((mdat_offsets[(segment.index, track_id)], end - start))
    return plans, payload_sizes


def _mdat_layout(layout: Layout, tables: list[SampleTable], prefix_size: int):
    """Compute every output mdat's absolute offset given header prefix size."""
    cursor = prefix_size
    offsets: dict[tuple[int, int], int] = {}
    ranges: list[tuple[int, int]] = []
    by_id = {t.track_id: t for t in tables}
    for segment in layout.segments:
        seg_start = cursor
        for track_id, (start, end) in segment.track_samples.items():
            payload = sum(by_id[track_id].sizes[i] for i in range(start, end))
            offsets[(segment.index, track_id)] = cursor + MDAT_HEADER
            cursor += MDAT_HEADER + payload
        ranges.append((seg_start, cursor))
    return offsets, ranges, cursor


def rebuild(input_path: str | Path, output_path: str | Path, segment_ms: int, max_drift_ms: int) -> RebuildResult:
    input_path = Path(input_path).resolve()
    output_path = Path(output_path).resolve()
    if not input_path.is_file():
        raise FileError(f"input not found: {input_path}")
    top, tables, mdats = _load(input_path)
    layout = plan_segments(tables, segment_ms, max_drift_ms)
    ftyp = next((b for b in top if b.type == "ftyp"), None)
    if ftyp is None:
        raise FormatError("input has no ftyp box")

    # Two passes: moov size (and thus whether co64 is needed) depends on the
    # final offsets, which themselves depend on the moov/sidx prefix length.
    with input_path.open("rb") as stream:
        ftyp_bytes = _box_bytes(stream, ftyp)
        moov_box = next(b for b in top if b.type == "moov")
        prefix = len(ftyp_bytes)
        segment_times = [(s.start_time_sidx, s.end_time_sidx) for s in layout.segments]

        # Pass 1: assume all offsets fit in 32 bits, measure the prefix.
        probe_offsets, _, _ = _mdat_layout(layout, tables, prefix)
        probe_plans, _ = _plan_chunks(layout, tables, probe_offsets)
        moov_32 = MoovRewriter(moov_box, stream, probe_plans).render()
        probe_sidx = build_sidx(tables, layout.sidx_timescale,
                                [(prefix + len(moov_32), prefix + len(moov_32) + 1)] * len(layout.segments),
                                segment_times, prefix + len(moov_32))
        sidx_32 = probe_sidx.serialize(prefix + len(moov_32))
        prefix_32 = prefix + len(moov_32) + len(sidx_32)

        # Pass 2: real offsets with the measured prefix; promote to co64 if
        # any chunk offset exceeds 32 bits, then measure once more for stability.
        offsets, ranges, total = _mdat_layout(layout, tables, prefix_32)
        needs_co64 = any(offset > 0xFFFFFFFF for offset in offsets.values())
        plans, payload_sizes = _plan_chunks(layout, tables, offsets)
        moov_bytes = MoovRewriter(moov_box, stream, plans).render()
        if needs_co64:
            grown_prefix = prefix + len(moov_bytes)
            offsets, ranges, total = _mdat_layout(layout, tables, grown_prefix + len(sidx_32))
            plans, payload_sizes = _plan_chunks(layout, tables, offsets)
            if any(offset > 0xFFFFFFFF for offset in offsets.values()):
                raise FormatError("chunk offsets exceed 64-bit addressable range")
            moov_bytes = MoovRewriter(moov_box, stream, plans).render()
        sidx_start = prefix + len(moov_bytes)
        sidx = build_sidx(tables, layout.sidx_timescale, ranges, segment_times, sidx_start)
        sidx_bytes = sidx.serialize(sidx_start)
        if sidx_start + len(sidx_bytes) != ranges[0][0]:
            raise ValidationError("SIDX prefix does not match planned mdat origin")
        _atomic_write(input_path, output_path, stream, ftyp_bytes, moov_bytes, sidx_bytes,
                      layout, tables, offsets, payload_sizes, total)
    return RebuildResult(output_path, layout, ranges, sidx, total)


def _box_bytes(stream: BinaryIO, box: Box) -> bytes:
    stream.seek(box.start)
    return stream.read(box.end - box.start)


def _atomic_write(input_path, output_path, src, ftyp_bytes, moov_bytes, sidx_bytes,
                  layout, tables, offsets, payload_sizes, expected_total) -> None:
    try:
        output_path.parent.mkdir(parents=True, exist_ok=True)
    except OSError as exc:
        raise FileError(f"cannot create output directory {output_path.parent}: {exc}") from exc
    tmp_fd, tmp_name = tempfile.mkstemp(prefix=output_path.name + ".", suffix=".tmp", dir=output_path.parent)
    tmp_path = Path(tmp_name)
    try:
        with os.fdopen(tmp_fd, "wb") as dst:
            dst.write(ftyp_bytes)
            dst.write(moov_bytes)
            dst.write(sidx_bytes)
            by_id = {t.track_id: t for t in tables}
            for segment in layout.segments:
                for track_id, (start, end) in segment.track_samples.items():
                    payload = payload_sizes[(segment.index, track_id)]
                    dst.write((MDAT_HEADER + payload).to_bytes(4, "big") + b"mdat")
                    for offset, size in collect_sample_bytes(by_id[track_id], start, end):
                        _copy_range(src, dst, offset, size)
            if dst.tell() != expected_total:
                raise ValidationError("output size mismatch during write")
        verify_bytes(tmp_path)
        os.replace(tmp_path, output_path)
    except (OSError, ValidationError, FormatError):
        tmp_path.unlink(missing_ok=True)
        raise


def _copy_range(src: BinaryIO, dst: BinaryIO, offset: int, size: int) -> None:
    src.seek(offset)
    remaining = size
    while remaining:
        chunk = src.read(min(IO_BUFFER, remaining))
        if not chunk:
            raise FileError(f"unexpected EOF reading source offset {offset}")
        dst.write(chunk)
        remaining -= len(chunk)
