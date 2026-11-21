"""Independent validation of a sidx against the sample table.

The validator deliberately re-runs the box, stbl and sidx parsers instead of
reusing any rebuild in-memory state.  It maps every sidx media reference back
to whole chunks, then checks, per reference:

* the start byte equals the first chunk's byte offset,
* the referenced size equals the summed size of the covered chunks,
* the reference duration equals the summed sample decode durations.

It also checks that references are contiguous, cover every chunk exactly once
and that their durations add up to the track duration.  Mismatches are reported
item by item; a single bad reference does not hide the others.
"""

from __future__ import annotations

from fractions import Fraction

from .box_reader import BoxReader
from .errors import ConsistencyError
from .model import Sidx, Track
from .report import ValidationReport
from .sidx import find_sidx, reference_offsets
from .stbl import find_mdat, parse_track


def validate_bytes(data: bytes, *, source: str = "<memory>") -> ValidationReport:
    view = memoryview(data)
    reader = BoxReader(view)
    roots = reader.parse_top_level()
    mdat = find_mdat(view, roots)
    track = parse_track(view, roots, mdat)
    # find_sidx already parsed and validated the box; reuse that result rather
    # than parsing it a second time.
    sidx = find_sidx(view, roots, required=True)
    assert sidx is not None
    return validate_parsed(source, track, sidx)


def validate_parsed(source: str, track: Track, sidx: Sidx) -> ValidationReport:
    report = ValidationReport(source=source)
    report.sample_entries = track.sample_count
    report.reference_count = sidx.reference_count
    report.timescale = sidx.timescale
    report.earliest_presentation_time = sidx.earliest_presentation_time
    report.index_version = sidx.version

    if not sidx.references:
        report.errors.append(
            f"box sidx at offset {sidx.box_start}: no media references"
        )
        return report

    ranges = reference_offsets(sidx)
    chunk_by_offset = {chunk.offset: chunk for chunk in track.chunks}
    chunk_by_end = {chunk.end: chunk for chunk in track.chunks}

    expected_chunk = track.chunks[0].index if track.chunks else 1
    mapped: list[tuple[int, int, int, int]] = []  # (first_sample, sample_end, chunk_first, chunk_last)

    for reference, (start, end) in zip(sidx.references, ranges):
        check = {
            "index": reference.index,
            "reference_type": reference.reference_type,
            "reference_offset": start,
            "referenced_size": reference.referenced_size,
            "expected_offset": None,
            "expected_size": None,
            "reference_duration": reference.subsegment_duration,
            "expected_duration": None,
            "status": "ok",
            "issues": [],
        }
        report.offset_checks.append(check)

        if not reference.is_media:
            check["status"] = "mismatch"
            check["issues"].append(
                "nested index references are not supported; expected a media reference"
            )
            report.errors.append(
                f"box sidx at offset {sidx.box_start}: reference {reference.index} "
                "is a nested index reference"
            )
            continue

        first_chunk = chunk_by_offset.get(start)
        if first_chunk is None:
            check["status"] = "mismatch"
            check["issues"].append(
                f"reference starts at {start}, which is not a chunk byte offset"
            )
            report.errors.append(
                f"box sidx at offset {sidx.box_start}: reference {reference.index} "
                f"starts at {start}, not at a chunk boundary"
            )
            continue

        if first_chunk.index != expected_chunk:
            check["status"] = "mismatch"
            check["issues"].append(
                f"reference starts at chunk {first_chunk.index} but chunk "
                f"{expected_chunk} is expected"
            )
            report.errors.append(
                f"box sidx at offset {sidx.box_start}: reference {reference.index} "
                f"skips or repeats chunks around chunk {expected_chunk}"
            )
            continue

        chunk = first_chunk
        size = 0
        while True:
            size += chunk.size
            if chunk.end > end:
                check["status"] = "mismatch"
                check["issues"].append(
                    f"reference ends at {end}, cutting chunk {chunk.index} "
                    f"which ends at {chunk.end}"
                )
                report.errors.append(
                    f"box sidx at offset {sidx.box_start}: reference "
                    f"{reference.index} cuts chunk {chunk.index} at byte {end}"
                )
                break
            if chunk.end == end:
                break
            next_index = chunk.index + 1
            if next_index > len(track.chunks):
                check["status"] = "mismatch"
                check["issues"].append(
                    f"reference ends at {end}, beyond the last chunk"
                )
                report.errors.append(
                    f"box sidx at offset {sidx.box_start}: reference "
                    f"{reference.index} extends beyond the media data"
                )
                break
            next_chunk = track.chunks[next_index - 1]
            if next_chunk.offset != chunk.end:
                check["status"] = "mismatch"
                check["issues"].append(
                    f"chunks {chunk.index} and {next_chunk.index} are not contiguous"
                )
                report.errors.append(
                    f"box sidx at offset {sidx.box_start}: media chunks are not "
                    "contiguous"
                )
                break
            chunk = next_chunk

        if check["status"] != "ok":
            continue

        last_chunk = chunk
        expected_size = size
        expected_duration = _to_timescale(
            sum(
                sample.duration
                for sample in track.samples[
                    first_chunk.first_sample : last_chunk.sample_end
                ]
            ),
            track.timescale,
            sidx.timescale,
        )
        check["expected_offset"] = first_chunk.offset
        check["expected_size"] = expected_size
        check["expected_duration"] = (
            None if expected_duration is None else expected_duration
        )
        if start != first_chunk.offset:
            check["status"] = "mismatch"
            check["issues"].append(
                f"reference offset {start} != chunk offset {first_chunk.offset}"
            )
        if reference.referenced_size != expected_size:
            check["status"] = "mismatch"
            check["issues"].append(
                f"referenced size {reference.referenced_size} != chunk span "
                f"{expected_size}"
            )
        if expected_duration is None:
            check["status"] = "mismatch"
            check["issues"].append(
                f"segment duration is not integral in sidx timescale {sidx.timescale}"
            )
        elif reference.subsegment_duration != expected_duration:
            check["status"] = "mismatch"
            check["issues"].append(
                f"subsegment duration {reference.subsegment_duration} != "
                f"sample duration {expected_duration}"
            )
        if check["status"] != "ok":
            report.errors.append(
                f"box sidx at offset {sidx.box_start}: reference "
                f"{reference.index} offset/size/duration do not match the "
                "sample table"
            )

        mapped.append(
            (
                first_chunk.first_sample,
                last_chunk.sample_end,
                first_chunk.index,
                last_chunk.index,
            )
        )
        expected_chunk = last_chunk.index + 1

    if len(mapped) == len(sidx.references) and len(mapped) == len(report.offset_checks):
        report.boundaries = [[first, last] for first, last, _a, _b in mapped]

    covered_chunks = sum(last - first + 1 for _s, _e, first, last in mapped)
    report.coverage_check = {
        "status": "ok"
        if covered_chunks == len(track.chunks) and not report.errors
        else "mismatch",
        "covered_chunks": covered_chunks,
        "total_chunks": len(track.chunks),
    }
    if covered_chunks != len(track.chunks) and not report.errors:
        report.errors.append(
            f"box sidx at offset {sidx.box_start}: references cover "
            f"{covered_chunks} of {len(track.chunks)} chunks"
        )
        report.coverage_check["status"] = "mismatch"

    sidx_total = sum(reference.subsegment_duration for reference in sidx.references)
    track_total = _to_timescale(track.duration, track.timescale, sidx.timescale)
    if track_total is None:
        report.duration_check = {
            "status": "mismatch",
            "sidx_total": sidx_total,
            "track_total": None,
            "reason": f"track duration is not integral in sidx timescale "
            f"{sidx.timescale}",
        }
        report.errors.append(
            f"box sidx at offset {sidx.box_start}: track duration does not "
            f"convert integrally to timescale {sidx.timescale}"
        )
    else:
        status = "ok" if sidx_total == track_total else "mismatch"
        report.duration_check = {
            "status": status,
            "sidx_total": sidx_total,
            "track_total": track_total,
        }
        if status != "ok":
            report.errors.append(
                f"box sidx at offset {sidx.box_start}: reference durations sum "
                f"to {sidx_total} but the track lasts {track_total} ticks"
            )

    if report.boundaries and report.boundaries[-1][1] != track.sample_count:
        report.errors.append(
            f"box sidx at offset {sidx.box_start}: references cover samples up "
            f"to {report.boundaries[-1][1]} but the track has "
            f"{track.sample_count}"
        )
    return report


def _to_timescale(value: int, from_scale: int, to_scale: int) -> int | None:
    if from_scale <= 0 or to_scale <= 0:
        raise ConsistencyError(
            f"invalid timescale conversion {from_scale} -> {to_scale}",
            box_type=b"sidx",
        )
    scaled = Fraction(value * to_scale, from_scale)
    if scaled.denominator != 1:
        return None
    return int(scaled)


def require_valid(report: ValidationReport) -> None:
    if report.ok:
        return
    first = report.errors[0] if report.errors else "index does not match sample table"
    raise ConsistencyError(first, box_type=b"sidx")
