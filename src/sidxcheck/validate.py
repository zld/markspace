"""Validate resolved sidx references against the file and rebuilt segments.

Every check here consumes the :class:`~sidxcheck.model.SidxIndex` produced by
:mod:`sidxcheck.sidx` directly; no offset is recomputed, so parser and
validator share one error classification.
"""

from __future__ import annotations

from . import model


def validate_references(
    sidx: model.SidxIndex,
    segments: list[model.RebuiltSegment],
    file_size: int,
    media_end: int,
    scan,
) -> list[model.OffsetCheck]:
    """Check ordering, offsets, sizes, ranges and trailing data."""
    checks: list[model.OffsetCheck] = []
    refs = sidx.references
    seg_by_ref = {seg.reference_index: seg for seg in segments}

    previous_end: int | None = None
    previous_offset: int | None = None
    for ref in refs:
        start = ref.target_offset
        end = start + ref.referenced_size
        mismatches: list[str] = []

        if previous_offset is not None:
            if start == previous_offset:
                scan.fail(
                    model.REF_DUPLICATE,
                    f"reference {ref.index} starts at the same offset {start} "
                    "as the preceding reference",
                    box_offset=sidx.box_offset,
                    box_type="sidx",
                    entry_index=ref.index,
                )
                mismatches.append("duplicate")
            elif start < previous_offset:
                scan.fail(
                    model.REF_NON_MONOTONIC,
                    f"reference {ref.index} starts at {start}, before the "
                    f"preceding reference at {previous_offset}",
                    box_offset=sidx.box_offset,
                    box_type="sidx",
                    entry_index=ref.index,
                )
                mismatches.append("non_monotonic")
        if previous_end is not None and start < previous_end:
            scan.fail(
                model.REF_OVERLAP,
                f"reference {ref.index} [{start}, {end}) overlaps the "
                f"preceding reference ending at {previous_end}",
                box_offset=sidx.box_offset,
                box_type="sidx",
                entry_index=ref.index,
            )
            mismatches.append("overlap")

        if start < 0 or end > file_size or end <= start:
            scan.fail(
                model.REF_OUT_OF_RANGE,
                f"reference {ref.index} [{start}, {end}) is outside the file "
                f"(size {file_size})",
                box_offset=sidx.box_offset,
                box_type="sidx",
                entry_index=ref.index,
            )
            mismatches.append("out_of_range")
        else:
            segment = seg_by_ref.get(ref.index)
            if segment is None:
                scan.fail(
                    model.REF_SAMPLE_COVERAGE_INCOMPLETE,
                    f"reference {ref.index} has no rebuilt sample coverage",
                    box_offset=sidx.box_offset,
                    box_type="sidx",
                    entry_index=ref.index,
                )
                mismatches.append("no_coverage")
            else:
                if segment.start != start:
                    scan.fail(
                        model.REF_OFFSET_MISMATCH,
                        f"reference {ref.index} points at {start} but its "
                        f"first sample starts at {segment.start}",
                        box_offset=sidx.box_offset,
                        box_type="sidx",
                        entry_index=ref.index,
                    )
                    mismatches.append("offset")
                rebuilt_size = segment.end - segment.start
                if rebuilt_size != ref.referenced_size:
                    scan.fail(
                        model.REF_SIZE_MISMATCH,
                        f"reference {ref.index} declares "
                        f"{ref.referenced_size} bytes but its samples occupy "
                        f"{rebuilt_size} bytes",
                        box_offset=sidx.box_offset,
                        box_type="sidx",
                        entry_index=ref.index,
                    )
                    mismatches.append("size")
                if segment.end != end:
                    scan.fail(
                        model.REF_SAMPLE_COVERAGE_INCOMPLETE,
                        f"reference {ref.index} ends at {end} but sample "
                        f"coverage ends at {segment.end}",
                        box_offset=sidx.box_offset,
                        box_type="sidx",
                        entry_index=ref.index,
                    )
                    mismatches.append("coverage_end")

        checks.append(
            model.OffsetCheck(
                reference_index=ref.index,
                passed=not mismatches,
                expected_offset=start,
                actual_offset=(
                    seg_by_ref[ref.index].start
                    if ref.index in seg_by_ref else None
                ),
                declared_size=ref.referenced_size,
                end_offset=end,
            )
        )
        previous_offset = start
        previous_end = end

    # Trailing bytes after the media region are unreferenced payload.
    if refs and media_end:
        last_ref_end = refs[-1].target_offset + refs[-1].referenced_size
        boundary = max(media_end, last_ref_end)
        if boundary < file_size:
            scan.fail(
                model.TRAILING_DATA,
                f"{file_size - boundary} trailing bytes from offset {boundary} "
                "to EOF are not referenced by any sidx entry",
                box_offset=boundary,
                box_type="sidx",
            )

    return checks
