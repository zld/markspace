"""Cross-validation that re-runs the self-contained parse layer.

Validation is intentionally tolerant: each check is independent, records a
:class:`CheckResult` and never aborts the remaining checks when one fails.
Fatal structural errors (the file cannot be parsed at all) are caught at the
top level and converted to a single failed check with the parse reason.
"""

from __future__ import annotations

from dataclasses import dataclass, field

from .errors import MediaIndexError
from .model import (
    STATUS_FAIL,
    STATUS_PASS,
    CheckResult,
    Rational,
    StblTables,
    ticks_to_rational,
)
from .rebuild import RebuildResult, rebuild_timeline
from .stbl_map import build_chunk_spans

# Stable check names; they are part of the JSON contract.
CHK_BOX_STRUCTURE = "box_structure"
CHK_SIDX_OFFSETS = "sidx_offset_checks"
CHK_SIDX_DURATIONS = "sidx_duration_checks"
CHK_TABLE_COUNTS = "table_sample_counts"
CHK_CHUNK_OFFSETS = "chunk_offset_monotonicity"
CHK_CHUNK_COVERAGE = "chunk_sample_coverage"
CHK_CTTS = "ctts_offsets"
CHK_STSS = "sync_sample_table"
CHK_TIMELINE = "segment_rebuild"
CHK_TIMESCALE = "timescale"
CHK_TIME_OVERFLOW = "time_accumulation"
CHK_SIDX_NESTING = "sidx_nesting"


def _check_to_dict(c: CheckResult) -> dict:
    return {"name": c.name, "status": c.status, "source_offset": c.source_offset,
            "reason": c.reason, "details": c.details}


@dataclass
class ValidationReport:
    path: str
    checks: list[CheckResult] = field(default_factory=list)
    sample_entries: int = 0
    segment_boundaries: list[dict] = field(default_factory=list)
    errors: list[str] = field(default_factory=list)

    @property
    def passed(self) -> int:
        return sum(1 for c in self.checks if c.status == STATUS_PASS)

    @property
    def failed(self) -> int:
        return sum(1 for c in self.checks if c.status == STATUS_FAIL)

    def add(self, name: str, status: str, source_offset: int,
            reason: str = "", details: dict | None = None) -> CheckResult:
        result = CheckResult(name, status, source_offset, reason, details or {})
        self.checks.append(result)
        if status == STATUS_FAIL:
            self.errors.append(
                f"{name}@{source_offset}: {reason}"
                if source_offset >= 0 else f"{name}: {reason}"
            )
        return result

    def to_dict(self) -> dict:
        return {
            "path": self.path,
            "status": STATUS_PASS if self.failed == 0 else STATUS_FAIL,
            "sample_entries": self.sample_entries,
            "index_offset_checks": {
                "passed": self._count(CHK_SIDX_OFFSETS, STATUS_PASS),
                "failed": self._count(CHK_SIDX_OFFSETS, STATUS_FAIL),
            },
            "segment_boundaries": self.segment_boundaries,
            "checks": [_check_to_dict(c) for c in self.checks],
            "errors": self.errors,
        }

    def _count(self, name: str, status: str) -> int:
        return sum(
            1 for c in self.checks if c.name == name and c.status == status
        )


def validate_parsed(
    path: str,
    view: memoryview,
    tree,
    tables_list: list[StblTables],
    file_size: int,
) -> ValidationReport:
    """Run every independent cross-check over already-parsed structures."""

    report = ValidationReport(path=path)
    report.sample_entries = (
        tables_list[0].sample_count if tables_list else 0
    )

    _check_timescale(report, tree)
    _check_sidx_offsets(report, tree, file_size)
    _check_sidx_durations(report, tree)
    _check_nesting(report, tree)
    _check_tables(report, tables_list)

    rebuild_result: RebuildResult | None = None
    if tables_list:
        try:
            spans = build_chunk_spans(tables_list[0])
            rebuild_result = rebuild_timeline(tree, spans, file_size)
            _check_rebuild(report, rebuild_result)
        except MediaIndexError as exc:
            report.add(CHK_TIMELINE, STATUS_FAIL, exc.offset, exc.reason)
    else:
        report.add(CHK_TIMELINE, STATUS_FAIL, -1, "no stbl tables found")

    if rebuild_result is not None:
        report.segment_boundaries = [
            {
                "order": b.order,
                "source_offset": b.source_offset,
                "byte_start": b.byte_start,
                "byte_end": b.byte_end,
                "first_sample": b.first_sample,
                "last_sample": b.last_sample,
                "complete": b.complete,
            }
            for b in rebuild_result.boundaries
        ]
    return report


def _check_timescale(report: ValidationReport, tree) -> None:
    box = tree.box
    if box.timescale <= 0 or box.timescale > (1 << 32) - 1:
        report.add(CHK_TIMESCALE, STATUS_FAIL, box.box_start,
                   f"illegal timescale {box.timescale}")
    else:
        report.add(CHK_TIMESCALE, STATUS_PASS, box.box_start,
                    details={"timescale": box.timescale})


def _check_sidx_offsets(report: ValidationReport, tree, file_size: int) -> None:
    for node in tree.walk():
        box = node.box
        for ref in box.references:
            if ref.byte_start < 0 or ref.byte_end < ref.byte_start:
                report.add(CHK_SIDX_OFFSETS, STATUS_FAIL, ref.source_offset,
                           f"illegal range [{ref.byte_start}, {ref.byte_end})")
            elif ref.byte_end > file_size:
                report.add(CHK_SIDX_OFFSETS, STATUS_FAIL, ref.source_offset,
                           f"range ends at {ref.byte_end} beyond file size {file_size}")
            else:
                report.add(CHK_SIDX_OFFSETS, STATUS_PASS, ref.source_offset,
                            details={"byte_start": ref.byte_start,
                                     "byte_end": ref.byte_end})


def _check_sidx_durations(report: ValidationReport, tree) -> None:
    for node in tree.walk():
        box = node.box
        total = 0
        for ref in box.media_references:
            if ref.subsegment_duration == 0:
                report.add(CHK_SIDX_DURATIONS, STATUS_FAIL, ref.source_offset,
                           "media reference declares zero subsegment_duration")
            total += ref.subsegment_duration
        try:
            ticks_to_rational(total, box.timescale)
            report.add(CHK_SIDX_DURATIONS, STATUS_PASS, box.box_start,
                        details={"total_ticks": total,
                                 "timescale": box.timescale})
        except ValueError as exc:
            report.add(CHK_SIDX_DURATIONS, STATUS_FAIL, box.box_start, str(exc))


def _check_nesting(report: ValidationReport, tree) -> None:
    nodes = tree.walk()
    starts = [n.box.box_start for n in nodes]
    if len(starts) != len(set(starts)):
        report.add(CHK_SIDX_NESTING, STATUS_FAIL, tree.box.box_start,
                   "duplicate sidx positions indicate a reference cycle")
    else:
        report.add(CHK_SIDX_NESTING, STATUS_PASS, tree.box.box_start,
                    details={"sidx_boxes": len(nodes)})


def _check_tables(report: ValidationReport, tables_list: list[StblTables]) -> None:
    for tables in tables_list:
        for check in (_check_counts, _check_chunk_offsets, _check_coverage,
                      _check_ctts, _check_stss, _check_time_overflow):
            check(report, tables)


def _check_counts(report: ValidationReport, tables: StblTables) -> None:
    if tables.stts.total_samples != tables.sample_count:
        report.add(CHK_TABLE_COUNTS, STATUS_FAIL, tables.stts.source_offset,
                   f"stts expands to {tables.stts.total_samples} samples but "
                   f"size table declares {tables.sample_count}")
    else:
        report.add(CHK_TABLE_COUNTS, STATUS_PASS, tables.size_table.source_offset,
                    details={"samples": tables.sample_count})


def _check_chunk_offsets(report: ValidationReport, tables: StblTables) -> None:
    offsets = tables.chunk_offsets.offsets
    duplicates = sorted({o for o in offsets if offsets.count(o) > 1})
    non_monotonic = [
        i for i in range(1, len(offsets)) if offsets[i] < offsets[i - 1]
    ]
    if duplicates:
        report.add(CHK_CHUNK_OFFSETS, STATUS_FAIL,
                   tables.chunk_offsets.source_offset,
                   f"duplicate chunk offset(s): {duplicates[:8]}")
    elif non_monotonic:
        report.add(CHK_CHUNK_OFFSETS, STATUS_FAIL,
                   tables.chunk_offsets.source_offset,
                   f"chunk offsets decrease at index(es) {non_monotonic[:8]}")
    else:
        report.add(CHK_CHUNK_OFFSETS, STATUS_PASS,
                   tables.chunk_offsets.source_offset,
                   details={"chunks": len(offsets)})


def _check_coverage(report: ValidationReport, tables: StblTables) -> None:
    try:
        spans = build_chunk_spans(tables)
    except MediaIndexError as exc:
        report.add(CHK_CHUNK_COVERAGE, STATUS_FAIL, exc.offset, exc.reason)
        return
    covered = sum(s.last_sample - s.first_sample + 1 for s in spans)
    if covered != tables.sample_count:
        report.add(CHK_CHUNK_COVERAGE, STATUS_FAIL, tables.stsc.source_offset,
                   f"chunks cover {covered} samples, size table has "
                   f"{tables.sample_count}")
    else:
        report.add(CHK_CHUNK_COVERAGE, STATUS_PASS, tables.stsc.source_offset,
                    details={"chunks": len(spans), "samples": covered})


def _check_ctts(report: ValidationReport, tables: StblTables) -> None:
    ctts = tables.ctts
    if ctts is None:
        report.add(CHK_CTTS, STATUS_PASS, tables.stbl_offset,
                    details={"present": False})
        return
    if ctts.total_samples != tables.sample_count:
        report.add(CHK_CTTS, STATUS_FAIL, ctts.source_offset,
                   f"ctts covers {ctts.total_samples} samples, expected "
                   f"{tables.sample_count}")
        return
    # Version 0 offsets are unsigned; any negative value is impossible there.
    if ctts.version == 0 and any(e.sample_offset < 0 for e in ctts.entries):
        report.add(CHK_CTTS, STATUS_FAIL, ctts.source_offset,
                   "version 0 ctts carries a negative offset")
    else:
        report.add(CHK_CTTS, STATUS_PASS, ctts.source_offset,
                    details={"version": ctts.version, "entries": len(ctts.entries)})


def _check_stss(report: ValidationReport, tables: StblTables) -> None:
    stss = tables.stss
    if stss is None:
        report.add(CHK_STSS, STATUS_PASS, tables.stbl_offset,
                    details={"present": False, "sync_implicit": True})
        return
    out_of_range = [s for s in stss.sync_samples if s > tables.sample_count]
    if out_of_range:
        report.add(CHK_STSS, STATUS_FAIL, stss.source_offset,
                   f"sync sample number(s) out of range: {out_of_range[:8]}")
    else:
        report.add(CHK_STSS, STATUS_PASS, stss.source_offset,
                    details={"sync_samples": len(stss.sync_samples)})


def _check_time_overflow(report: ValidationReport, tables: StblTables) -> None:
    stts = tables.stts
    try:
        value: Rational = ticks_to_rational(stts.total_ticks, 1)  # structural check
        report.add(CHK_TIME_OVERFLOW, STATUS_PASS, stts.source_offset,
                    details={"total_ticks": value.numerator})
    except ValueError as exc:
        report.add(CHK_TIME_OVERFLOW, STATUS_FAIL, stts.source_offset, str(exc))


def _check_rebuild(report: ValidationReport, result: RebuildResult) -> None:
    if not result.issues:
        report.add(CHK_TIMELINE, STATUS_PASS,
                   result.boundaries[0].source_offset if result.boundaries else -1,
                    details={"segments": len(result.boundaries)})
        return
    for issue in result.issues:
        report.add(CHK_TIMELINE, STATUS_FAIL, issue.source_offset, issue.reason)
