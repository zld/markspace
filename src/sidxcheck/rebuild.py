"""Rebuild sample byte ranges and fragment boundaries from the sample table.

The rebuilder trusts nothing beyond the parsed stbl layout: each sample range
is checked against the file boundary, and every sidx reference is matched
against the samples that actually fall inside its declared span.
"""

from __future__ import annotations

from . import model
from .stbl import sample_size_at


def _sample_end(table: model.SampleTable, index: int) -> int:
    return table.sample_offsets[index] + sample_size_at(table, index)


def rebuild(
    table: model.SampleTable,
    sidx: model.SidxIndex,
    file_size: int,
    scan,
) -> tuple[list[model.RebuiltSegment], int]:
    """Return (rebuilt segments, end offset of the last located sample)."""
    total = table.total_samples

    # Per-sample sanity: offsets must be ordered and ranges inside the file.
    media_end = 0
    for i in range(total):
        start = table.sample_offsets[i]
        end = _sample_end(table, i)
        if i > 0 and start < table.sample_offsets[i - 1]:
            scan.fail(
                model.REF_OUT_OF_RANGE,
                f"sample {i} starts at {start}, before the previous sample",
                box_type="stbl",
                entry_index=i,
            )
            break
        if end > file_size or start >= file_size or end < start:
            scan.fail(
                model.REF_OUT_OF_RANGE,
                f"sample {i} byte range [{start}, {end}) exceeds file size "
                f"{file_size}",
                box_type="stbl",
                entry_index=i,
            )
            break
        media_end = max(media_end, end)

    segments: list[model.RebuiltSegment] = []
    sample_cursor = 0

    for ref in sidx.references:
        ref_start = ref.target_offset
        ref_end = ref_start + ref.referenced_size

        first_sample: int | None = None
        last_sample = -1
        i = sample_cursor
        while i < total:
            s_start = table.sample_offsets[i]
            s_end = _sample_end(table, i)
            if s_start >= ref_end:
                break
            if s_end > ref_start:  # the sample overlaps the reference span
                if first_sample is None:
                    first_sample = i
                last_sample = i
                if s_start < ref_start or s_end > ref_end:
                    scan.fail(
                        model.CROSS_BOUNDARY_SAMPLE,
                        f"sample {i} [{s_start}, {s_end}) crosses the boundary "
                        f"of reference {ref.index} [{ref_start}, {ref_end})",
                        box_type="sidx",
                        entry_index=ref.index,
                    )
            i += 1

        if first_sample is None:
            scan.fail(
                model.REF_SAMPLE_COVERAGE_INCOMPLETE,
                f"reference {ref.index} [{ref_start}, {ref_end}) covers no "
                "samples in the sample table",
                box_type="sidx",
                entry_index=ref.index,
            )
            continue

        # A sample overlapping this span but starting before it means the
        # previous reference did not fully contain its own samples.
        if first_sample > sample_cursor:
            scan.fail(
                model.REF_SAMPLE_COVERAGE_INCOMPLETE,
                f"samples {sample_cursor}..{first_sample - 1} are not covered "
                f"by any reference before reference {ref.index}",
                box_type="sidx",
                entry_index=ref.index,
            )

        seg_start = table.sample_offsets[first_sample]
        seg_end = _sample_end(table, last_sample)
        if seg_start != ref_start or seg_end != ref_end:
            scan.fail(
                model.REF_SAMPLE_COVERAGE_INCOMPLETE,
                f"reference {ref.index} declares [{ref_start}, {ref_end}) but "
                f"its samples span [{seg_start}, {seg_end})",
                box_type="sidx",
                entry_index=ref.index,
            )

        segments.append(
            model.RebuiltSegment(
                reference_index=ref.index,
                start=seg_start,
                end=seg_end,
                first_sample=first_sample,
                last_sample=last_sample,
            )
        )
        sample_cursor = last_sample + 1

    if sample_cursor < total:
        scan.fail(
            model.REF_SAMPLE_COVERAGE_INCOMPLETE,
            f"samples {sample_cursor}..{total - 1} are not covered by any "
            "sidx reference",
            box_type="sidx",
        )

    return segments, media_end
