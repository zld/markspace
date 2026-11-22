"""Cross-check the sample table against media bytes and the sidx index.

Structural problems in the sample table (out-of-range samples, chunk map
vs stsz count mismatch, truncated media) are errors and forbid a rebuild.
A missing, truncated or outdated sidx is a recoverable warning: the index
may be rebuilt from a valid sample table.
"""
from __future__ import annotations

import hashlib
import os
import struct
from typing import BinaryIO

from .box import Box, BoxError, MAX_FILE_BYTES, iter_boxes, read_payload
from .model import SampleTable, Sidx, ValidationReport
from .sidx import decode_sidx
from .stbl import load_sample_table

FIXTURE_BRAND = "mpk1"
SAMPLE_MAGIC = b"MPKT"
SAMPLE_HEAD = 32  # magic(4) + index(4) + payload_len(4) + sha1(20)
_MAX_SAMPLE_READ = 64 * 1024 * 1024  # bound per-sample digest reads


def validate_file(path: str | os.PathLike) -> ValidationReport:
    report = ValidationReport(path=str(path))
    try:
        size = os.path.getsize(path)
    except OSError as exc:
        report.error("io-error", str(exc))
        return report
    if size > MAX_FILE_BYTES:
        report.error("file-too-large", f"{size} bytes exceeds the 2 GiB limit")
        return report
    try:
        with open(path, "rb") as fh:
            top = {b.type: b for b in iter_boxes(fh, 0, size)}
            moov, mdat = top.get("moov"), top.get("mdat")
            if moov is None or mdat is None:
                report.error("missing-box", "file lacks moov or mdat")
                return report
            brand = None
            if "ftyp" in top:
                payload = read_payload(fh, top["ftyp"])
                if len(payload) >= 4:
                    brand = payload[:4].decode("latin-1")
            table = load_sample_table(fh, moov)
            report.table = table
            report.sample_count = table.sample_count
            report.timescale = table.timescale
            _check_sample_ranges(report, table, mdat)
            if brand == FIXTURE_BRAND:
                _check_sample_digests(fh, report, table)
            sidx_box = top.get("sidx")
            if sidx_box is None:
                report.warning("index-absent", "no sidx box; index must be rebuilt")
                report.index_offset_check = "ABSENT"
            else:
                _check_sidx(fh, report, table, sidx_box)
    except BoxError as exc:
        report.error("structure", str(exc))
    except OSError as exc:
        report.error("io-error", str(exc))
    return report


def _check_sample_ranges(report: ValidationReport, table: SampleTable, mdat: Box) -> None:
    for s in table.samples:
        if s.offset < mdat.payload_start or s.offset + s.size > mdat.end:
            report.error(
                "sample-range",
                f"sample {s.index} spans [{s.offset}, {s.offset + s.size}) "
                f"outside mdat payload [{mdat.payload_start}, {mdat.end})",
            )


def _check_sample_digests(
    fh: BinaryIO, report: ValidationReport, table: SampleTable
) -> None:
    """Verify embedded per-sample digests in recognizable fixtures.

    Catches offsets that are numerically in-range but point at the wrong
    bytes (e.g. a stale chunk-offset layout).
    """
    for s in table.samples:
        if s.size > _MAX_SAMPLE_READ:
            report.warning(
                "sample-skipped", f"sample {s.index} too large for digest check"
            )
            continue
        fh.seek(s.offset)
        data = fh.read(s.size)
        if len(data) != s.size or not verify_fixture_sample(s.index, data):
            report.error(
                "sample-digest",
                f"sample {s.index} content digest mismatch at offset {s.offset}",
            )


def verify_fixture_sample(index: int, data: bytes) -> bool:
    if len(data) < SAMPLE_HEAD or data[:4] != SAMPLE_MAGIC:
        return False
    stored_index, payload_len = struct.unpack_from(">II", data, 4)
    digest = data[12:SAMPLE_HEAD]
    payload = data[SAMPLE_HEAD:]
    return (
        stored_index == index
        and payload_len == len(payload)
        and hashlib.sha1(payload).digest() == digest
    )


def _check_sidx(
    fh: BinaryIO, report: ValidationReport, table: SampleTable, sidx_box: Box
) -> None:
    try:
        sidx = decode_sidx(read_payload(fh, sidx_box), sidx_box.start, sidx_box.size)
    except BoxError as exc:
        report.warning("index-stale", f"sidx not decodable: {exc}")
        report.index_offset_check = "STALE"
        return
    report.sidx = sidx
    problems = match_references(table, sidx)
    if problems:
        report.warning("index-stale", "; ".join(problems))
        report.index_offset_check = "STALE"
    else:
        report.index_offset_check = "PASS"


def match_references(table: SampleTable, sidx: Sidx) -> list[str]:
    """Check that sidx references tile the sample sequence exactly.

    Each reference must cover a contiguous run of samples, start where the
    previous one ended (anchored at box_end + first_offset), carry the
    summed sample duration, and start on a sync sample when flagged SAP.
    """
    problems: list[str] = []
    samples = table.samples
    if sidx.timescale != table.timescale:
        problems.append(
            f"sidx timescale {sidx.timescale} != stbl timescale {table.timescale}"
        )
    if samples and sidx.earliest_presentation_time != samples[0].dts:
        problems.append(
            f"earliest presentation time {sidx.earliest_presentation_time} "
            f"!= first sample dts {samples[0].dts}"
        )
    pos = sidx.box_end + sidx.first_offset
    si = 0
    for ri, ref in enumerate(sidx.references):
        if si >= len(samples):
            problems.append(f"reference {ri + 1} has no samples left to cover")
            break
        run, acc = [], 0
        while si < len(samples) and acc < ref.size:
            run.append(samples[si])
            acc += samples[si].size
            si += 1
        if acc != ref.size:
            problems.append(
                f"reference {ri + 1} size {ref.size} does not align with "
                f"sample sizes (accumulated {acc})"
            )
            break
        duration = sum(s.duration for s in run)
        if duration != ref.duration:
            problems.append(
                f"reference {ri + 1} duration {ref.duration} != sample sum {duration}"
            )
        if pos != run[0].offset:
            problems.append(
                f"reference {ri + 1} starts at {pos} but samples begin at "
                f"{run[0].offset}"
            )
        if ref.starts_with_sap and not run[0].is_sync:
            problems.append(
                f"reference {ri + 1} flagged SAP but sample {run[0].index} "
                f"is not a sync sample"
            )
        pos += ref.size
    if si != len(samples):
        problems.append(f"references cover {si} of {len(samples)} samples")
    return problems
