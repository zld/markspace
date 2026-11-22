"""Fragment planning and atomic sidx rebuild from a valid sample table.

Fragments are closed at the nearest sync sample at or after the target
duration, never splitting a sample; when no sync sample is available near
the target the fragment is extended to the next one and the overshoot is
recorded as the fragment's delta. The new file is written to a temporary
file, re-parsed and re-validated, and only then atomically replaces the
original. Rebuild is refused when the sample table itself is invalid.
"""
from __future__ import annotations

import os
import struct
import tempfile
from dataclasses import dataclass, field

from .box import Box, iter_boxes
from .model import Fragment, Sample, SampleTable, SidxReference, ValidationReport
from .sidx import encode_sidx
from .stbl import (
    encode_co64,
    encode_mdhd,
    encode_stco,
    encode_stsc,
    encode_stss,
    encode_stsz,
    encode_stts,
    expand_sample_table,
)
from .validate import validate_file

_COPY_CHUNK = 1024 * 1024


@dataclass
class RebuildResult:
    ok: bool
    report: ValidationReport
    fragments: list[Fragment] = field(default_factory=list)
    reference_count: int = 0
    message: str = ""


def compute_fragments(samples: list[Sample], target_units: int) -> list[Fragment]:
    if target_units <= 0:
        raise ValueError("target duration must be positive")
    if not samples:
        raise ValueError("no samples to fragment")
    if not samples[0].is_sync:
        raise ValueError("first sample is not a sync sample; cannot open a fragment")
    fragments: list[Fragment] = []
    start = 0
    acc = 0
    for i, sample in enumerate(samples):
        acc += sample.duration
        nxt = samples[i + 1] if i + 1 < len(samples) else None
        if acc >= target_units and (nxt is None or nxt.is_sync):
            fragments.append(
                Fragment(samples[start].index, sample.index, acc, acc - target_units)
            )
            start = i + 1
            acc = 0
    if start < len(samples):
        acc = sum(s.duration for s in samples[start:])
        fragments.append(
            Fragment(samples[start].index, samples[-1].index, acc, acc - target_units)
        )
    return fragments


def _box(type_: bytes, payload: bytes) -> bytes:
    return struct.pack(">I4s", 8 + len(payload), type_) + payload


def build_moov(table: SampleTable) -> bytes:
    sizes = [s.size for s in table.samples]
    stbl = b"".join(
        (
            encode_stts(table.stts_entries),
            encode_stsc(table.stsc_entries),
            encode_stsz(sizes),
            encode_stco(table.chunk_offsets)
            if table.offset_width == 4
            else encode_co64(table.chunk_offsets),
            encode_stss(list(table.sync_indices)),
        )
    )
    mdia = _box(
        b"mdia",
        encode_mdhd(table.timescale, table.total_duration)
        + _box(b"minf", _box(b"stbl", stbl)),
    )
    return _box(b"moov", _box(b"trak", mdia))


def _plan_layout(
    path: str, table: SampleTable, fragments: list[Fragment]
) -> tuple[bytes, Box, list[SidxReference]]:
    """Compute the new ftyp+sidx+moov prefix and the mdat span to copy.

    The rewritten file keeps only ftyp, the new sidx, moov and mdat; any
    other top-level boxes (including a stale sidx) are dropped.
    """
    size = os.path.getsize(path)
    with open(path, "rb") as fh:
        top = {b.type: b for b in iter_boxes(fh, 0, size)}
        mdat = top["mdat"]
        ftyp_bytes = b""
        if "ftyp" in top:
            ftyp = top["ftyp"]
            fh.seek(ftyp.start)
            ftyp_bytes = fh.read(ftyp.size)
    refs = []
    for frag in fragments:
        seg = table.samples[frag.start_sample - 1 : frag.end_sample]
        refs.append(
            SidxReference(
                size=sum(s.size for s in seg),
                duration=frag.duration,
                starts_with_sap=seg[0].is_sync,
                sap_delta_time=0,
            )
        )
    sidx_size = 32 + 12 * len(refs)  # 8 header + 24 fixed payload + 12 per ref
    moov_draft = build_moov(table)  # size is independent of offset values
    new_payload_start = len(ftyp_bytes) + sidx_size + len(moov_draft) + mdat.header
    delta = new_payload_start - mdat.payload_start
    new_offsets = [o + delta for o in table.chunk_offsets]
    shifted = expand_sample_table(
        table.timescale,
        [s.size for s in table.samples],
        table.stsc_entries,
        new_offsets,
        table.stts_entries,
        set(table.sync_indices),
        table.offset_width,
    )
    sidx_end = len(ftyp_bytes) + sidx_size
    first_offset = shifted.samples[0].offset - sidx_end
    sidx_bytes = encode_sidx(
        table.timescale, table.samples[0].dts, first_offset, refs
    )
    if len(sidx_bytes) != sidx_size:
        raise AssertionError("sidx size drifted from plan")
    prefix = ftyp_bytes + sidx_bytes + build_moov(shifted)
    return prefix, mdat, refs


def rebuild_file(path: str, target_seconds: float) -> RebuildResult:
    report = validate_file(path)
    if not report.ok or report.table is None:
        return RebuildResult(
            False, report, message="sample table invalid; rebuild refused"
        )
    table = report.table
    try:
        target_units = round(target_seconds * table.timescale)
        fragments = compute_fragments(table.samples, target_units)
        prefix, mdat, refs = _plan_layout(str(path), table, fragments)
    except ValueError as exc:
        report.error("fragmentation", str(exc))
        return RebuildResult(False, report, message=str(exc))
    directory = os.path.dirname(os.path.abspath(path))
    fd, tmp = tempfile.mkstemp(prefix=".mediapack-", dir=directory)
    try:
        with os.fdopen(fd, "wb") as out, open(path, "rb") as src:
            out.write(prefix)
            src.seek(mdat.start)
            remaining = mdat.size
            while remaining > 0:
                chunk = src.read(min(_COPY_CHUNK, remaining))
                if not chunk:
                    raise OSError("unexpected EOF while copying mdat")
                out.write(chunk)
                remaining -= len(chunk)
        check = validate_file(tmp)
        if not check.ok or check.index_offset_check != "PASS":
            os.unlink(tmp)
            return RebuildResult(
                False,
                check,
                message="rebuilt file failed re-validation; original untouched",
            )
        os.replace(tmp, path)
    except BaseException:
        if os.path.exists(tmp):
            os.unlink(tmp)
        raise
    return RebuildResult(
        True, validate_file(path), fragments, reference_count=len(refs)
    )
