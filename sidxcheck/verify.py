"""Whole-file verification shared by ``check``, ``rebuild`` and ``demo``."""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path
from typing import BinaryIO

from .boxes import Box, BoxTree, parse_boxes, read_payload
from .errors import IndexValidationError, MediaStructureError
from .samplemap import SampleMap, build_sample_map
from .sidx import Sidx, parse_sidx_box, validate_sidx_structure
from .tables import load_track_tables, mdat_ranges


@dataclass
class IndexCheck:
    box_path: str
    box_start: int
    box_end: int
    version: int
    reference_count: int
    timescale: int
    reference_id: int
    first_offset: int
    anchor_offset: int
    expected_anchor: int | None
    offset_delta: int | None
    offset_ok: bool
    sample_count: int
    duration_ok: bool
    problems: list[str] = field(default_factory=list)

    @property
    def ok(self) -> bool:
        return self.offset_ok and self.duration_ok and not self.problems


@dataclass
class CheckResult:
    path: str
    file_size: int
    track_id: int
    timescale: int
    media_duration: int
    sample_entries: int
    sample_map: SampleMap
    sidx_box_count: int
    index_checks: list[IndexCheck] = field(default_factory=list)
    problems: list[str] = field(default_factory=list)
    warnings: list[str] = field(default_factory=list)

    @property
    def ok(self) -> bool:
        return not self.problems and all(check.ok for check in self.index_checks)

    @property
    def primary_index(self) -> IndexCheck | None:
        return self.index_checks[0] if self.index_checks else None


def _match_reference_to_samples(
    sidx: Sidx,
    sample_map: SampleMap,
    check: IndexCheck,
    *,
    require_full_coverage: bool,
) -> int:
    """Walk SIDX references against sample byte ranges; return covered samples."""
    samples = sample_map.samples
    by_start = {sample.offset: index for index, sample in enumerate(samples)}
    position = sidx.anchor_offset
    covered = 0
    last_covered_index = -1
    for ref_index, reference in enumerate(sidx.references):
        where = f"reference[{ref_index}]"
        start_index = by_start.get(position)
        if start_index is None:
            check.problems.append(
                f"{where} starts at byte {position}, which is not a sample boundary "
                f"(sample {last_covered_index + 2} starts at "
                f"{samples[last_covered_index + 1].offset if last_covered_index + 1 < len(samples) else 'EOF'})"
            )
            return covered
        end = position + reference.referenced_size
        cursor = position
        duration = 0
        index = start_index
        while cursor < end:
            if index >= len(samples):
                check.problems.append(
                    f"{where} ends at {end} beyond the last sample byte range"
                )
                return covered
            sample = samples[index]
            if sample.offset != cursor:
                check.problems.append(
                    f"{where} cuts into sample {index + 1}: expected its start "
                    f"{sample.offset}, reference continues at {cursor}"
                )
                return covered
            if sample.end > end:
                check.problems.append(
                    f"{where} ends mid-sample {index + 1} at {end} "
                    f"(sample ends at {sample.end})"
                )
                return covered
            duration += sample.duration
            cursor = sample.end
            index += 1
        if duration != reference.subsegment_duration:
            check.problems.append(
                f"{where} declares {reference.subsegment_duration} ticks but its "
                f"samples sum to {duration}"
            )
        if reference.starts_with_sap:
            first = samples[start_index]
            if not first.is_sync:
                check.problems.append(
                    f"{where} claims SAP but sample {start_index + 1} is not a sync sample"
                )
        covered += index - start_index
        last_covered_index = index - 1
        position = end
    if require_full_coverage:
        if covered != len(samples):
            check.problems.append(
                f"SIDX references cover {covered} of {len(samples)} samples"
            )
        if position != samples[-1].end:
            check.problems.append(
                f"SIDX references end at byte {position} but sample data ends at "
                f"{samples[-1].end}"
            )
    return covered


def _check_sidx(
    sidx: Sidx,
    box: Box,
    sample_map: SampleMap,
    *,
    require_full_coverage: bool,
) -> IndexCheck:
    check = IndexCheck(
        box_path=box.path,
        box_start=box.start,
        box_end=box.end,
        version=sidx.version,
        reference_count=len(sidx.references),
        timescale=sidx.timescale,
        reference_id=sidx.reference_id,
        first_offset=sidx.first_offset,
        anchor_offset=sidx.anchor_offset,
        expected_anchor=sample_map.samples[0].offset if sample_map.samples else None,
        offset_delta=None,
        offset_ok=False,
        sample_count=0,
        duration_ok=False,
    )
    if check.expected_anchor is not None:
        check.offset_delta = sidx.anchor_offset - check.expected_anchor
        check.offset_ok = check.offset_delta == 0
        if not check.offset_ok:
            check.problems.append(
                f"first reference starts at byte {sidx.anchor_offset} "
                f"(box end {box.end} + first_offset {sidx.first_offset}) but the first "
                f"sample starts at {check.expected_anchor} "
                f"(delta {check.offset_delta:+d})"
            )
    if sidx.reference_id != sample_map.track_id:
        check.problems.append(
            f"reference_ID {sidx.reference_id} does not match track_ID "
            f"{sample_map.track_id}"
        )
    if sidx.timescale != sample_map.timescale:
        check.problems.append(
            f"SIDX timescale {sidx.timescale} does not match mdhd timescale "
            f"{sample_map.timescale}"
        )
    check.problems.extend(validate_sidx_structure(sidx))
    if sidx.timescale == sample_map.timescale:
        check.duration_ok = sidx.total_duration == sample_map.media_duration
        if not check.duration_ok:
            check.problems.append(
                f"SIDX durations sum to {sidx.total_duration} ticks but the track "
                f"lasts {sample_map.media_duration} ticks "
                f"(delta {sidx.total_duration - sample_map.media_duration:+d})"
            )
    if sidx.timescale != sample_map.timescale:
        check.duration_ok = False
    check.sample_count = _match_reference_to_samples(
        sidx, sample_map, check, require_full_coverage=require_full_coverage
    )
    return check


def check_file(
    path: Path,
    *,
    track_id: int,
    require_index: bool = True,
    require_full_coverage: bool = True,
    max_depth: int = 16,
    max_boxes: int = 200_000,
) -> CheckResult:
    try:
        with path.open("rb") as fp:
            file_size = path.stat().st_size
            tree = parse_boxes(fp, file_size, max_depth=max_depth, max_boxes=max_boxes)
            result = check_open_file(
                fp,
                tree,
                path=path,
                track_id=track_id,
                require_index=require_index,
                require_full_coverage=require_full_coverage,
            )
    except OSError as exc:
        from .errors import IOErrorFailure

        raise IOErrorFailure(f"cannot read {path}: {exc}", location=str(path)) from exc
    return result


def check_open_file(
    fp: BinaryIO,
    tree: BoxTree,
    *,
    path: Path,
    track_id: int,
    require_index: bool = True,
    require_full_coverage: bool = True,
) -> CheckResult:
    tables = load_track_tables(fp, tree, track_id)
    sample_map = build_sample_map(tables, mdat_ranges=mdat_ranges(tree))
    result = CheckResult(
        path=str(path),
        file_size=tree.file_size,
        track_id=track_id,
        timescale=sample_map.timescale,
        media_duration=sample_map.media_duration,
        sample_entries=sample_map.sample_count,
        sample_map=sample_map,
        sidx_box_count=0,
        warnings=list(sample_map.warnings),
    )
    sidx_boxes = [box for box in tree.boxes if box.type == "sidx"]
    top_level = [box for box in tree.all("sidx") if box.depth == 0]
    if len(top_level) != len(sidx_boxes):
        raise MediaStructureError(
            "sidx boxes nested inside other boxes cannot be expanded", location="sidx"
        )
    result.sidx_box_count = len(sidx_boxes)
    if require_index and not sidx_boxes:
        raise IndexValidationError(
            f"{path} has no SIDX box; run rebuild to create one", location="sidx"
        )
    for box in sidx_boxes:
        sidx = parse_sidx_box(fp, box)
        result.index_checks.append(
            _check_sidx(
                sidx,
                box,
                sample_map,
                require_full_coverage=require_full_coverage,
            )
        )
    if len(sidx_boxes) > 1:
        result.warnings.append(f"file contains {len(sidx_boxes)} SIDX boxes")
    if not result.index_checks and require_index:
        result.problems.append("no index was checked")
    return result


def summarize_offset(result: CheckResult) -> tuple[bool, int | None]:
    primary = result.primary_index
    if primary is None:
        return False, None
    return primary.offset_ok, primary.offset_delta
