"""Rebuild sample byte ranges and fragment boundaries from the sample table.

An sidx media reference covers a whole fragment box (typically ``mdat``): its
range includes the box header, while the stbl sample table only locates the
sample payload inside it.  The rebuilder therefore checks that every sample is
fully contained in exactly one reference, and that the remaining header gap
really is a well-formed fragment box rather than unaccounted payload.
"""

from __future__ import annotations

import struct

from . import model
from .stbl import sample_size_at

_HEADER_GAP = 8  # standard 32-bit-size box header inside a referenced fragment


def _sample_end(table: model.SampleTable, index: int) -> int:
    return table.sample_offsets[index] + sample_size_at(table, index)


def _verify_header_gap(stream, start: int, gap: int, end: int,
                       scan, ref_index: int) -> bool:
    """Confirm that ``[start, start+gap)`` is a fragment box header."""
    if gap == 0:
        return True
    if stream is None or gap not in (_HEADER_GAP, 16):
        scan.fail(
            model.REF_SAMPLE_COVERAGE_INCOMPLETE,
            f"reference {ref_index} leaves an unexplained {gap}-byte gap at "
            f"offset {start}",
            box_type="sidx",
            entry_index=ref_index,
        )
        return False
    stream.seek(start)
    head = stream.read(gap)
    if len(head) != gap or head[4:8] != b"mdat":
        scan.fail(
            model.REF_SAMPLE_COVERAGE_INCOMPLETE,
            f"reference {ref_index} gap at offset {start} is not an mdat box "
            "header",
            box_type="sidx",
            entry_index=ref_index,
        )
        return False
    if gap == _HEADER_GAP:
        (declared,) = struct.unpack(">I", head[:4])
        if declared != end - start:
            scan.fail(
                model.REF_SIZE_MISMATCH,
                f"reference {ref_index} spans {end - start} bytes but the mdat "
                f"header at {start} declares {declared}",
                box_offset=start,
                box_type="mdat",
                entry_index=ref_index,
            )
            return False
    return True


def rebuild(
    table: model.SampleTable,
    sidx: model.SidxIndex,
    file_size: int,
    scan,
    *,
    stream=None,
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
                if s_start < ref_start or s_end > ref_end:
                    scan.fail(
                        model.CROSS_BOUNDARY_SAMPLE,
                        f"sample {i} [{s_start}, {s_end}) is not fully inside "
                        f"reference {ref.index} [{ref_start}, {ref_end})",
                        box_type="sidx",
                        entry_index=ref.index,
                    )
                if first_sample is None:
                    first_sample = i
                last_sample = i
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

        if first_sample > sample_cursor:
            scan.fail(
                model.REF_SAMPLE_COVERAGE_INCOMPLETE,
                f"samples {sample_cursor}..{first_sample - 1} are not covered "
                f"by any reference before reference {ref.index}",
                box_type="sidx",
                entry_index=ref.index,
            )

        prefix_gap = table.sample_offsets[first_sample] - ref_start
        suffix_gap = ref_end - _sample_end(table, last_sample)
        _verify_header_gap(
            stream, ref_start, prefix_gap, ref_end, scan, ref.index
        )
        if suffix_gap != 0:
            scan.fail(
                model.REF_SAMPLE_COVERAGE_INCOMPLETE,
                f"reference {ref.index} has {suffix_gap} unexplained trailing "
                f"bytes after its last sample (ends {ref_end})",
                box_type="sidx",
                entry_index=ref.index,
            )

        segments.append(
            model.RebuiltSegment(
                reference_index=ref.index,
                start=ref_start,
                end=ref_end,
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
