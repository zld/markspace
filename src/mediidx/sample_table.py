"""Expand chunk / sample tables into concrete byte ranges.

The functions here consume already-parsed table entries and check that the
chunk count, sample count and byte coverage all agree.  Every inconsistency
carries the originating table's box offset.
"""

from __future__ import annotations

from .box_reader import Box
from .errors import ConsistencyError, StructureError
from .model import Chunk, Sample


def expand_chunks(
    offsets: list[int],
    samples_per_chunk: list[int],
    sizes: list[int],
    stsc_box: Box,
) -> list[Chunk]:
    if len(offsets) != len(samples_per_chunk):
        raise ConsistencyError(
            f"stsc describes {len(samples_per_chunk)} chunks but stco/co64 "
            f"describes {len(offsets)}",
            box_type=b"stsc",
            offset=stsc_box.start,
        )
    chunks: list[Chunk] = []
    sample_index = 0
    for chunk_index, (offset, count) in enumerate(
        zip(offsets, samples_per_chunk), start=1
    ):
        if sample_index + count > len(sizes):
            raise ConsistencyError(
                f"chunk {chunk_index} needs samples {sample_index}.."
                f"{sample_index + count - 1} but stsz only has {len(sizes)}",
                box_type=b"stsc",
                offset=stsc_box.start,
            )
        chunk_size = sum(sizes[sample_index : sample_index + count])
        chunks.append(
            Chunk(
                index=chunk_index,
                offset=offset,
                size=chunk_size,
                first_sample=sample_index,
                sample_count=count,
            )
        )
        sample_index += count
    if sample_index != len(sizes):
        raise ConsistencyError(
            f"stsc/stco cover {sample_index} samples but stsz declares "
            f"{len(sizes)}",
            box_type=b"stsc",
            offset=stsc_box.start,
        )
    return chunks


def samples_per_chunk(
    entries: list[tuple[int, int, int]], chunk_count: int, stsc_start: int
) -> list[int]:
    if not entries:
        raise StructureError("stsc has no entries", box_type=b"stsc", offset=stsc_start)
    counts: list[int] = []
    for position, (first_chunk, samples_per_chunk, _descriptor) in enumerate(entries):
        next_first = (
            entries[position + 1][0] if position + 1 < len(entries) else chunk_count + 1
        )
        if first_chunk > chunk_count:
            raise ConsistencyError(
                f"stsc references chunk {first_chunk} but there are only "
                f"{chunk_count} chunks",
                box_type=b"stsc",
                offset=stsc_start,
            )
        counts.extend([samples_per_chunk] * (next_first - first_chunk))
    return counts


def assign_times(
    samples: list[tuple[int, int, int]], stts: list[tuple[int, int]], stts_start: int
) -> list[Sample]:
    """Expand stts entries over the (offset, size, chunk) tuples."""
    result: list[Sample] = []
    position = 0
    dts = 0
    for run_count, delta in stts:
        for _ in range(run_count):
            if position >= len(samples):
                raise ConsistencyError(
                    f"stts describes more than the {len(samples)} samples in stsz",
                    box_type=b"stts",
                    offset=stts_start,
                )
            offset, size, chunk_index = samples[position]
            result.append(
                Sample(
                    index=position,
                    offset=offset,
                    size=size,
                    duration=delta,
                    dts=dts,
                    chunk_index=chunk_index,
                )
            )
            dts += delta
            position += 1
    if position != len(samples):
        raise ConsistencyError(
            f"stts covers {position} samples but stsz declares {len(samples)}",
            box_type=b"stts",
            offset=stts_start,
        )
    return result


def check_chunk_ranges(chunks: list[Chunk], mdat_start: int, mdat_end: int) -> None:
    previous_end = mdat_start
    for chunk in chunks:
        if chunk.size <= 0:
            raise ConsistencyError(
                f"chunk {chunk.index} has non-positive size {chunk.size}",
                box_type=b"stsz",
                offset=mdat_start,
            )
        if chunk.offset < mdat_start or chunk.end > mdat_end:
            raise ConsistencyError(
                f"chunk {chunk.index} byte range [{chunk.offset},{chunk.end}) "
                f"falls outside the mdat payload [{mdat_start},{mdat_end})",
                box_type=b"stco",
                offset=chunk.offset,
            )
        if chunk.offset < previous_end:
            raise ConsistencyError(
                f"chunk {chunk.index} starts at {chunk.offset}, before the "
                f"previous chunk ends at {previous_end}",
                box_type=b"stco",
                offset=chunk.offset,
            )
        previous_end = chunk.end
