"""Independent re-read validation of a rebuilt MP4.

The validator never trusts data structures carried over from the rebuild pass:
it re-parses the output file from disk, re-expands every sample table and
re-decodes the SIDX, then checks sample offsets, reference byte ranges, segment
coverage (no gaps, no overlaps) and SAP semantics.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path

from .boxes import Box, parse_file
from .errors import FormatError, ValidationError
from .sample_tables import VIDEO_HANDLER, SampleTable, parse_trak, validate_mdat_coverage
from .sidx import Sidx, SidxParseError, parse_sidx


@dataclass
class Verification:
    sample_entries: int
    sample_offsets_ok: tuple[int, int]
    sidx_refs: int
    sidx_offsets_ok: tuple[int, int]
    segment_count: int
    timescale: int
    segments: list[dict] = field(default_factory=list)
    drift_ms: float = 0.0

    def as_summary(self) -> dict:
        return {
            "sample_entries": self.sample_entries,
            "sample_offsets_ok": f"{self.sample_offsets_ok[0]}/{self.sample_offsets_ok[1]}",
            "sidx_refs": self.sidx_refs,
            "sidx_offsets_ok": f"{self.sidx_offsets_ok[0]}/{self.sidx_offsets_ok[1]}",
            "segment_count": self.segment_count,
            "drift_ms": self.drift_ms,
        }


def verify_bytes(path: str | Path) -> Verification:
    path = Path(path)
    top = parse_file(path)
    moov = _require(top, "moov")
    sidx_box = next((b for b in top if b.type == "sidx"), None)
    mdats = [b for b in top if b.type == "mdat"]
    if sidx_box is None:
        raise ValidationError("output contains no SIDX box")
    if not mdats:
        raise ValidationError("output contains no mdat boxes")
    with path.open("rb") as stream:
        tables = [parse_trak(trak, stream) for trak in moov.find_all("trak")]
        validate_mdat_coverage(tables, mdats)
        try:
            sidx = parse_sidx(stream, sidx_box)
        except SidxParseError as exc:
            raise ValidationError(f"SIDX unreadable in output: {exc}") from exc
        verification = _cross_check(tables, mdats, sidx, sidx_box, path.stat().st_size)
    return verification


def _require(top: list[Box], box_type: str) -> Box:
    box = next((b for b in top if b.type == box_type), None)
    if box is None:
        raise ValidationError(f"output missing required box {box_type}")
    return box


def _cross_check(tables: list[SampleTable], mdats: list[Box], sidx: Sidx, sidx_box: Box, file_size: int) -> Verification:
    total_samples = sum(t.count for t in tables)
    # 1. Every sample offset resolves inside exactly one mdat; no overlaps.
    mdat_ranges = sorted((m.start, m.end) for m in mdats)
    sample_spans: list[tuple[int, int]] = []
    for table in tables:
        for index in range(table.count):
            start = table._sample_offsets[index]
            end = start + table.sizes[index]
            if not any(lo <= start and end <= hi for lo, hi in mdat_ranges):
                raise ValidationError(f"track {table.track_id} sample {index} outside mdat coverage")
            sample_spans.append((start, end))
    sample_spans.sort()
    for (s1, e1), (s2, e2) in zip(sample_spans, sample_spans[1:]):
        if s2 < e1:
            raise ValidationError(f"overlapping sample byte ranges [{s1},{e1}) vs [{s2},{e2})")
    offsets_ok = total_samples

    # 2. SIDX references: contiguous, exactly covering all mdat bytes.
    ref_ranges = sidx.byte_ranges(sidx_box.start)
    mdat_span_start = mdat_ranges[0][0]
    mdat_span_end = mdat_ranges[-1][1]
    for (lo, hi), (nlo, nhi) in zip(mdat_ranges, mdat_ranges[1:]):
        if hi != nlo:
            raise ValidationError(f"mdat boxes not contiguous: gap/overlap between {hi} and {nlo}")
    if ref_ranges[0][0] != mdat_span_start or ref_ranges[-1][1] != mdat_span_end:
        raise ValidationError("SIDX references do not span the full mdat region")
    cursor = ref_ranges[0][0]
    for start, end in ref_ranges:
        if start != cursor or end <= start:
            raise ValidationError("SIDX references are not contiguous or have non-positive size")
        cursor = end
    if any(not ref.starts_with_sap for ref in sidx.references):
        raise ValidationError("every segment must start with a stream access point")
    if sidx.timescale <= 0:
        raise ValidationError("SIDX timescale must be positive")

    # 3. Times: references reproduce contiguous non-overlapping segment timing.
    segments: list[dict] = []
    time_cursor = sidx.earliest_presentation_time
    for index, (ref, (start, end)) in enumerate(zip(sidx.references, ref_ranges)):
        if ref.subsegment_duration <= 0:
            raise ValidationError(f"SIDX reference {index} has non-positive duration")
        segments.append(
            {
                "index": index,
                "start_ms": _to_ms(time_cursor, sidx.timescale),
                "end_ms": _to_ms(time_cursor + ref.subsegment_duration, sidx.timescale),
                "start_offset": start,
                "end_offset": end,
                "size": end - start,
            }
        )
        time_cursor += ref.subsegment_duration
    # 4. Every sample belongs to exactly one reference byte range.
    covered = 0
    for start, end in ref_ranges:
        for s, e in sample_spans:
            if start <= s and e <= end:
                covered += 1
    if covered != total_samples:
        raise ValidationError(f"only {covered}/{total_samples} samples fall inside SIDX references")
    if mdat_span_end > file_size:
        raise ValidationError("mdat region extends past end of file")
    return Verification(
        sample_entries=total_samples,
        sample_offsets_ok=(total_samples, total_samples),
        sidx_refs=len(ref_ranges),
        sidx_offsets_ok=(len(ref_ranges), len(ref_ranges)),
        segment_count=len(ref_ranges),
        timescale=sidx.timescale,
        segments=segments,
    )


def _to_ms(ticks: int, timescale: int) -> int:
    value = ticks * 1000
    if value % timescale:
        raise ValidationError(f"SIDX time {ticks} not integral in milliseconds for timescale {timescale}")
    return value // timescale
