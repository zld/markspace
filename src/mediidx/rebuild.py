"""Rewrite an MP4 with a sidx derived from its own sample table.

The source file is never modified.  The rebuilt file is assembled in memory,
re-parsed and validated, written to a temporary file in the destination
directory, re-validated from disk, and only then atomically renamed onto the
requested output path.  Any failure removes the temporary file and any partial
output, so a failed run cannot leave something that looks like success.

Layout rule: the new sidx is inserted directly after ``ftyp`` (or at the start
of the file when there is no ftyp) and every previous top-level sidx is
removed.  The chunk offset tables are patched by the net byte movement, so the
sample data keeps its exact bytes and only the addresses change.
"""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from fractions import Fraction

from .box_reader import Box, BoxReader, read_file_bytes
from .errors import ConsistencyError, StructureError, UsageError
from .layout import plan_layout, segment_boundaries
from .model import Layout, Sidx, Track
from .rewrite import atomic_write, assemble, patch_chunk_offsets, sidx_insert_at
from .sidx import build_references, encode_sidx, find_sidx
from .stbl import find_mdat, parse_track
from .validator import ValidationReport, validate_bytes

MAX_REFERENCES = 0xFFFF


@dataclass
class RebuildResult:
    source: str
    output: str
    sample_entries: int
    reference_count: int
    boundaries: list[list[int]]
    segments: list[dict]
    timescale: int
    version: int
    earliest_presentation_time: int
    first_offset: int
    index_offset_check: str
    duration_check: dict
    coverage_check: dict
    warnings: list[str] = field(default_factory=list)
    validation: ValidationReport | None = None

    def to_dict(self) -> dict:
        return {
            "command": "rebuild",
            "source": self.source,
            "output": self.output,
            "sample_entries": self.sample_entries,
            "reference_count": self.reference_count,
            "boundaries": self.boundaries,
            "segments": self.segments,
            "timescale": self.timescale,
            "sidx_version": self.version,
            "earliest_presentation_time": self.earliest_presentation_time,
            "first_offset": self.first_offset,
            "index_offset_check": self.index_offset_check,
            "duration_check": self.duration_check,
            "coverage_check": self.coverage_check,
            "warnings": self.warnings,
        }


# --------------------------------------------------------------------------
# Index field resolution
# --------------------------------------------------------------------------


def _existing_sidx(roots: list[Box], view: memoryview) -> Sidx | None:
    boxes = [box for box in roots if box.type == b"sidx"]
    if len(boxes) > 1:
        raise StructureError(
            f"expected at most one top-level sidx box, found {len(boxes)}",
            box_type=b"sidx",
            offset=boxes[1].start,
        )
    if not boxes:
        return None
    try:
        return find_sidx(view, roots)
    except StructureError:
        # A truncated or unreadable index is exactly why this tool exists; the
        # sample table remains authoritative and values that cannot be inferred
        # must come from the command line.
        return None


def _check_output_path(source: str, output: str) -> None:
    if os.path.abspath(source) == os.path.abspath(output):
        raise UsageError("--out must differ from the source file; it is never modified")
    parent = os.path.dirname(os.path.abspath(output))
    if not os.path.isdir(parent):
        raise UsageError(f"output directory does not exist: {parent}")
    if os.path.exists(output) and not os.path.isfile(output):
        raise UsageError(f"output path exists and is not a regular file: {output}")


def _layout_segments(layout: Layout) -> list[dict]:
    items: list[dict] = []
    for segment in layout.segments:
        items.append(
            {
                "index": segment.index,
                "sample_first": segment.sample_first,
                "sample_last": segment.sample_last,
                "byte_start": segment.byte_start,
                "byte_end": segment.byte_end,
                "start_time": segment.start_time,
                "duration": segment.duration,
                "chunks": [segment.chunk_first, segment.chunk_last],
                "isolated_reason": segment.isolated_reason,
            }
        )
    return items


def _parse_target_duration(value: Fraction | int | str) -> Fraction:
    if isinstance(value, Fraction):
        target = value
    elif isinstance(value, int):
        target = Fraction(value)
    else:
        text = str(value).strip()
        try:
            target = Fraction(text)
        except (ValueError, ZeroDivisionError):
            raise UsageError(f"invalid target duration {value!r}")
    if target <= 0:
        raise UsageError("--target-duration must be positive")
    return target


def rebuild(
    source: str,
    output: str,
    *,
    target_duration: Fraction | int | str,
    timescale: int | None = None,
    earliest_presentation_time: int | None = None,
    version: int | None = None,
    max_file_bytes: int | None = None,
) -> RebuildResult:
    _check_output_path(source, output)
    data = (
        read_file_bytes(source)
        if max_file_bytes is None
        else read_file_bytes(source, max_bytes=max_file_bytes)
    )
    view = memoryview(data)
    roots = BoxReader(view).parse_top_level(tolerate_broken_sidx=True)
    mdat = find_mdat(view, roots)
    track = parse_track(view, roots, mdat)
    existing = _existing_sidx(roots, view)

    resolved_timescale = timescale
    if resolved_timescale is None:
        if existing is None:
            raise UsageError(
                "--timescale is required when the input has no readable sidx"
            )
        resolved_timescale = existing.timescale
    if resolved_timescale <= 0:
        raise UsageError("--timescale must be a positive integer")

    resolved_earliest = earliest_presentation_time
    if resolved_earliest is None:
        resolved_earliest = (
            existing.earliest_presentation_time if existing is not None else 0
        )
    if resolved_earliest < 0:
        raise UsageError("--earliest-presentation-time must be non-negative")

    resolved_version = version
    if resolved_version is None:
        resolved_version = existing.version if existing is not None else 0
    if resolved_version not in (0, 1):
        raise UsageError("--sidx-version must be 0 or 1")

    layout = plan_layout(track, _parse_target_duration(target_duration))
    if len(layout.segments) > MAX_REFERENCES:
        raise UsageError(
            f"layout needs {len(layout.segments)} references but a sidx holds at "
            f"most {MAX_REFERENCES}"
        )

    probe = encode_sidx(
        version=resolved_version,
        reference_id=1,
        timescale=resolved_timescale,
        earliest_presentation_time=resolved_earliest,
        first_offset=0,
        references=build_references(layout.segments, resolved_timescale),
    )

    insert_at = sidx_insert_at(roots)
    removed_before_data = sum(
        box.size for box in roots if box.type == b"sidx" and box.start < mdat.start
    )
    delta = len(probe) - removed_before_data
    new_insert_at = insert_at - sum(
        box.size for box in roots if box.type == b"sidx" and box.end <= insert_at
    )
    first_offset = (track.chunks[0].offset + delta) - (new_insert_at + len(probe))
    if first_offset < 0:
        raise ConsistencyError(
            "sidx first_offset would be negative", box_type=b"sidx"
        )

    index_bytes = encode_sidx(
        version=resolved_version,
        reference_id=1,
        timescale=resolved_timescale,
        earliest_presentation_time=resolved_earliest,
        first_offset=first_offset,
        references=build_references(layout.segments, resolved_timescale),
    )
    if len(index_bytes) != len(probe):
        raise AssertionError("sidx size changed between probe and final encode")

    assembled = assemble(data, roots, index_bytes)
    if delta != 0:
        assembled = patch_chunk_offsets(assembled, track, delta)

    # Independent re-parse of the in-memory result, before touching the disk.
    validation = validate_bytes(assembled, source=output)
    if not validation.ok:
        raise ConsistencyError(
            "rebuilt index failed validation: "
            + (validation.errors[0] if validation.errors else "unknown mismatch"),
            box_type=b"sidx",
        )
    expected_boundaries = segment_boundaries(layout)
    if validation.boundaries != expected_boundaries:
        raise ConsistencyError(
            f"rebuilt index boundaries {validation.boundaries} do not match the "
            f"derived layout {expected_boundaries}",
            box_type=b"sidx",
        )

    # Write to a temporary file, validate from disk, then publish atomically.
    atomic_write(output, assembled)
    try:
        with open(output, "rb") as handle:
            written = handle.read()
        disk_validation = validate_bytes(written, source=output)
        if not disk_validation.ok:
            raise ConsistencyError(
                "written output failed re-validation: "
                + (
                    disk_validation.errors[0]
                    if disk_validation.errors
                    else "unknown mismatch"
                ),
                box_type=b"sidx",
            )
    except BaseException:
        try:
            os.unlink(output)
        except OSError:
            pass
        raise

    return RebuildResult(
        source=source,
        output=output,
        sample_entries=track.sample_count,
        reference_count=len(layout.segments),
        boundaries=expected_boundaries,
        segments=_layout_segments(layout),
        timescale=resolved_timescale,
        version=resolved_version,
        earliest_presentation_time=resolved_earliest,
        first_offset=first_offset,
        index_offset_check=validation.index_offset_check,
        duration_check=validation.duration_check,
        coverage_check=validation.coverage_check,
        warnings=list(layout.notes),
        validation=validation,
    )


def reference_ranges(result: RebuildResult) -> list[tuple[int, int]]:
    return [
        (segment["byte_start"], segment["byte_end"]) for segment in result.segments
    ]
