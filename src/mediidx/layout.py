"""Derive segment boundaries from the sample table and an mdat extent.

Splitting always happens on whole-chunk boundaries; ``mdat`` payload bytes are
never cut in the middle of a chunk.  The target duration is a packing
threshold, not a hard duration:

* a segment that starts with one normal chunk takes a second whole chunk, so a
  reference never degenerates into a raw per-chunk entry;
* after that it keeps taking chunks while the accumulated decode duration stays
  at or below the target, which selects the boundary closest to the target
  without crossing it;
* a chunk whose own duration already exceeds the target becomes its own
  segment and the reason is recorded in ``isolated_reason``.

Because boundaries are whole chunks, a segment may overshoot the target when
the second chunk is large.  The overshoot is explicit in each segment's
``duration`` and in the JSON ``warnings`` list.
"""

from __future__ import annotations

from fractions import Fraction

from .errors import UsageError
from .model import Layout, Segment, Track

MAX_SEGMENTS = 65_535  # one sidx box can hold at most 65535 references
MIN_CHUNKS_PER_SEGMENT = 2


def _chunk_duration(track: Track, chunk_index: int) -> int:
    chunk = track.chunks[chunk_index]
    return sum(
        track.samples[position].duration
        for position in range(chunk.first_sample, chunk.sample_end)
    )


def plan_layout(track: Track, target_ticks: Fraction | int) -> Layout:
    target = Fraction(target_ticks)
    if target <= 0:
        raise UsageError("target duration must be positive")
    if not track.chunks:
        raise UsageError("track has no chunks to plan")

    segments: list[Segment] = []
    notes: list[str] = []
    start = 0
    chunk_count = len(track.chunks)
    while start < chunk_count:
        first_duration = _chunk_duration(track, start)
        if first_duration > target:
            end = start + 1
            reason = (
                f"chunk {track.chunks[start].index} duration {first_duration} "
                f"exceeds target {target} and cannot be split"
            )
            notes.append(reason)
            isolated_reason: str | None = reason
        else:
            isolated_reason = None
            end = start + 1
            accumulated = first_duration
            while end < chunk_count and end - start < MIN_CHUNKS_PER_SEGMENT:
                next_duration = _chunk_duration(track, end)
                if next_duration > target:
                    # The oversized chunk must remain isolated; do not glue it
                    # to this segment just to reach the minimum group size.
                    break
                accumulated += next_duration
                end += 1
            while end < chunk_count:
                next_duration = _chunk_duration(track, end)
                if next_duration > target:
                    break
                if accumulated + next_duration > target:
                    # Stop at the closest whole-chunk boundary that does not
                    # exceed the threshold.
                    break
                accumulated += next_duration
                end += 1

        first_chunk = track.chunks[start]
        last_chunk = track.chunks[end - 1]
        duration = sum(
            sample.duration
            for sample in track.samples[first_chunk.first_sample : last_chunk.sample_end]
        )
        if isolated_reason is None and duration > target:
            notes.append(
                f"segment {len(segments)} duration {duration} overshoots target "
                f"{target}; whole-chunk boundaries only"
            )
        segments.append(
            Segment(
                index=len(segments),
                chunk_first=first_chunk.index,
                chunk_last=last_chunk.index,
                sample_first=first_chunk.first_sample,
                sample_last=last_chunk.sample_end,
                byte_start=first_chunk.offset,
                byte_end=last_chunk.end,
                start_time=track.samples[first_chunk.first_sample].dts,
                duration=duration,
                references=1,
                isolated_reason=isolated_reason,
            )
        )
        if len(segments) > MAX_SEGMENTS:
            raise UsageError(
                f"segment plan needs more than {MAX_SEGMENTS} sidx references"
            )
        start = end

    _check_partition(track, segments)
    return Layout(
        track=track,
        segments=segments,
        target_duration=target,
        notes=notes,
    )


def _check_partition(track: Track, segments: list[Segment]) -> None:
    """Every chunk and sample must appear in exactly one segment, in order."""

    expected_chunk = 1
    expected_sample = 0
    for segment in segments:
        if segment.chunk_first != expected_chunk:
            raise UsageError(
                f"segment {segment.index} starts at chunk {segment.chunk_first} "
                f"but chunk {expected_chunk} is unassigned"
            )
        if segment.sample_first != expected_sample:
            raise UsageError(
                f"segment {segment.index} starts at sample {segment.sample_first} "
                f"but sample {expected_sample} is unassigned"
            )
        if segment.byte_start < track.mdat_start or segment.byte_end > track.mdat_end:
            raise UsageError(
                f"segment {segment.index} byte range "
                f"[{segment.byte_start},{segment.byte_end}) escapes the mdat"
            )
        expected_chunk = segment.chunk_last + 1
        expected_sample = segment.sample_last
    if expected_chunk != len(track.chunks) + 1:
        raise UsageError("segment plan does not cover every chunk")
    if expected_sample != track.sample_count:
        raise UsageError("segment plan does not cover every sample")


def segment_boundaries(layout: Layout) -> list[list[int]]:
    return [[segment.sample_first, segment.sample_last] for segment in layout.segments]


def boundaries_from_references(
    track: Track, references: list[tuple[int, int]]
) -> list[list[int]]:
    """Map absolute ``[start, end)`` reference ranges back to sample ranges."""

    boundaries: list[list[int]] = []
    chunk_by_offset = {chunk.offset: chunk for chunk in track.chunks}
    expected_chunk = 1
    for ref_index, (start, end) in enumerate(references):
        first = chunk_by_offset.get(start)
        if first is None:
            raise UsageError(
                f"reference {ref_index} starts at {start}, which is not a chunk "
                "boundary"
            )
        if first.index != expected_chunk:
            raise UsageError(
                f"reference {ref_index} starts at chunk {first.index} but chunk "
                f"{expected_chunk} is expected"
            )
        chunk = first
        while True:
            if chunk.end > end:
                raise UsageError(
                    f"reference {ref_index} cuts chunk {chunk.index} at {end}"
                )
            if chunk.end == end:
                break
            nxt = chunk.index + 1
            if nxt > len(track.chunks):
                raise UsageError(
                    f"reference {ref_index} ends at {end}, beyond the last chunk"
                )
            chunk = track.chunks[nxt - 1]
            if chunk.offset != track.chunks[nxt - 2].end:
                raise UsageError(
                    f"reference {ref_index} spans a gap before chunk {chunk.index}"
                )
        boundaries.append([first.first_sample, chunk.sample_end])
        expected_chunk = chunk.index + 1
    if expected_chunk != len(track.chunks) + 1:
        raise UsageError(
            f"sidx references cover chunks 1..{expected_chunk - 1} but the "
            f"track has {len(track.chunks)} chunks"
        )
    return boundaries
