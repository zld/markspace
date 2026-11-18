"""Rebuild continuous sample boundaries from sidx byte ranges.

The rebuilder takes leaf media references of a resolved sidx tree and the
chunk spans derived from the stbl tables, then decides which samples belong
to which segment. Rules enforced here:

* adjacent references may share a zero-length boundary position;
* positive-length byte ranges must never overlap;
* a chunk must sit fully inside one reference, or the segment is marked
  incomplete with the offending chunk's source offset reported;
* every referenced byte range must exist inside the file.
"""

from __future__ import annotations

from dataclasses import dataclass, field

from .errors import ParseError
from .model import (
    SidxReference,
    SidxTree,
    SegmentBoundary,
)
from .stbl_map import ChunkSpan


@dataclass
class RebuildIssue:
    reference_order: int
    source_offset: int
    reason: str


@dataclass
class RebuildResult:
    boundaries: tuple[SegmentBoundary, ...] = ()
    issues: tuple[RebuildIssue, ...] = ()
    unassigned_chunks: tuple[int, ...] = field(default_factory=tuple)


def rebuild_timeline(
    tree: SidxTree,
    chunk_spans: tuple[ChunkSpan, ...],
    file_size: int,
) -> RebuildResult:
    """Map leaf sidx media references onto chunk/sample boundaries."""

    refs = tree.leaf_media_references()
    issues: list[RebuildIssue] = []
    boundaries: list[SegmentBoundary] = []
    assigned_chunks: set[int] = set()

    prev_end = -1
    for ref in refs:
        if ref.byte_end > file_size:
            issues.append(
                RebuildIssue(
                    ref.order,
                    ref.source_offset,
                    f"reference range [{ref.byte_start}, {ref.byte_end}) exceeds "
                    f"file size {file_size}",
                )
            )
        if ref.byte_start < prev_end:
            issues.append(
                RebuildIssue(
                    ref.order,
                    ref.source_offset,
                    f"positive-length overlap: range starts at {ref.byte_start} "
                    f"before previous end {prev_end}",
                )
            )
        if ref.referenced_size > 0:
            prev_end = ref.byte_end

        members, incomplete = _chunks_in_range(ref, chunk_spans)
        for idx in members:
            assigned_chunks.add(idx)
        if incomplete:
            issues.append(
                RebuildIssue(
                    ref.order,
                    ref.source_offset,
                    "chunk(s) cross the segment boundary: "
                    + ", ".join(f"chunk {i + 1}" for i in incomplete),
                )
            )
        first_sample = chunk_spans[members[0]].first_sample if members else None
        last_sample = chunk_spans[members[-1]].last_sample if members else None
        boundaries.append(
            SegmentBoundary(
                order=ref.order,
                source_offset=ref.source_offset,
                byte_start=ref.byte_start,
                byte_end=ref.byte_end,
                first_sample=first_sample,
                last_sample=last_sample,
                complete=not incomplete and bool(members),
            )
        )

    unassigned = tuple(
        idx for idx in range(len(chunk_spans)) if idx not in assigned_chunks
    )
    if unassigned:
        first = chunk_spans[unassigned[0]]
        issues.append(
            RebuildIssue(
                0,
                -1,
                f"{len(unassigned)} chunk(s) not covered by any sidx reference "
                f"(first: chunk {first.chunk_index + 1} at offset {first.offset})",
            )
        )
    return RebuildResult(
        boundaries=tuple(boundaries),
        issues=tuple(issues),
        unassigned_chunks=unassigned,
    )


def _chunks_in_range(
    ref: SidxReference, spans: tuple[ChunkSpan, ...]
) -> tuple[list[int], list[int]]:
    """Return (member chunk indexes, indexes that straddle the boundary)."""

    members: list[int] = []
    incomplete: list[int] = []
    for span in spans:
        chunk_end = span.offset + span.length
        if span.offset >= ref.byte_start and chunk_end <= ref.byte_end:
            members.append(span.chunk_index)
        elif span.offset < ref.byte_end and chunk_end > ref.byte_start:
            # Overlaps the range but is not fully contained: boundary crossing.
            incomplete.append(span.chunk_index)
            members.append(span.chunk_index)
        elif span.offset == ref.byte_end and span.length == 0:
            # Zero-length chunk may legally sit exactly on the shared boundary.
            continue
    if incomplete:
        return members, incomplete
    return members, []


def ensure_adjacency(refs: tuple[SidxReference, ...]) -> None:
    """Raise if two positive-length references leave a gap or overlap."""

    cursor = refs[0].byte_start if refs else 0
    for ref in refs:
        if ref.referenced_size == 0:
            continue
        if ref.byte_start != cursor:
            raise ParseError(
                f"reference {ref.order} starts at {ref.byte_start}, expected {cursor}",
                ref.source_offset,
            )
        cursor = ref.byte_end
