"""Sample map construction and cross-table consistency checks."""

from __future__ import annotations

from dataclasses import dataclass, field

from .errors import IndexValidationError, MediaStructureError
from .samples import SampleTables

MAX_CTTS_OFFSET = 1 << 31


@dataclass(frozen=True)
class Sample:
    index: int
    offset: int
    size: int
    duration: int
    dts: int
    cts: int
    is_sync: bool
    chunk_index: int

    @property
    def end(self) -> int:
        return self.offset + self.size

    @property
    def composition_end(self) -> int:
        return self.cts + self.duration


@dataclass
class SampleMap:
    track_id: int
    timescale: int
    samples: list[Sample]
    media_duration: int
    mdhd_duration: int
    composition_offset_max: int = 0
    composition_offset_min: int = 0
    warnings: list[str] = field(default_factory=list)

    @property
    def sample_count(self) -> int:
        return len(self.samples)

    @property
    def sync_indices(self) -> list[int]:
        return [i for i, s in enumerate(self.samples) if s.is_sync]

    @property
    def presentation_duration(self) -> int:
        if not self.samples:
            return 0
        return max(s.composition_end for s in self.samples)

    def byte_ranges(self) -> list[tuple[int, int, int]]:
        return [(s.offset, s.end, s.index) for s in self.samples]


def _expand_counts(
    entries: list[tuple[int, int]], sample_count: int, what: str
) -> list[int]:
    """Expand (count, value) run-length entries to exactly sample_count values."""
    values: list[int] = []
    for index, (count, value) in enumerate(entries):
        if count < 0:
            raise MediaStructureError(
                f"{what} entry {index} has negative count {count}", location=what
            )
        values.extend([value] * count)
        if len(values) > sample_count:
            break
    if len(values) != sample_count:
        raise IndexValidationError(
            f"{what} covers {len(values)} samples but the track has {sample_count}; "
            f"{'missing' if len(values) < sample_count else 'extra'} "
            f"{abs(sample_count - len(values))} sample(s)",
            location=what,
        )
    return values


def _chunk_sample_counts(tables: SampleTables) -> list[int]:
    chunk_count = len(tables.chunk_offsets)
    if chunk_count == 0:
        if tables.sample_sizes:
            raise IndexValidationError(
                f"track {tables.track_id} has {len(tables.sample_sizes)} samples but 0 chunks",
                location="stco/co64",
            )
        return []
    counts: list[int] = []
    stsc = tables.stsc
    if not stsc:
        raise IndexValidationError("stsc has no entries", location="stsc")
    for position, entry in enumerate(stsc):
        if entry.first_chunk > chunk_count:
            if position == 0:
                raise IndexValidationError(
                    f"stsc first_chunk {entry.first_chunk} exceeds chunk count {chunk_count}",
                    location="stsc",
                )
            # A trailing unused entry is malformed but harmless; the previous
            # run simply does not extend past the last chunk.
            break
        next_first = (
            stsc[position + 1].first_chunk if position + 1 < len(stsc) else chunk_count + 1
        )
        if next_first <= entry.first_chunk:
            raise IndexValidationError(
                f"stsc run starting at chunk {entry.first_chunk} has no length",
                location="stsc",
            )
        last_chunk = min(next_first - 1, chunk_count)
        counts.extend([entry.samples_per_chunk] * (last_chunk - entry.first_chunk + 1))
    if len(counts) < chunk_count:
        counts.extend([stsc[-1].samples_per_chunk] * (chunk_count - len(counts)))
    if len(counts) != chunk_count:
        raise IndexValidationError(
            f"stsc maps {len(counts)} chunks but stco/co64 lists {chunk_count}",
            location="stsc",
        )
    return counts


def build_sample_map(
    tables: SampleTables,
    *,
    mdat_ranges: list[tuple[int, int]],
) -> SampleMap:
    sample_count = len(tables.sample_sizes)
    if not 0 < sample_count <= 1_000_000:
        raise IndexValidationError(
            f"track {tables.track_id} has {sample_count} samples, expected 1..1000000",
            location="stbl",
        )
    for index, size in enumerate(tables.sample_sizes):
        if size == 0:
            raise IndexValidationError(
                f"sample {index} (1-based {index + 1}) has zero length", location="stsz/stz2"
            )
    durations = _expand_counts(
        [(e.sample_count, e.sample_delta) for e in tables.stts], sample_count, "stts"
    )
    for index, duration in enumerate(durations):
        if duration == 0:
            raise IndexValidationError(
                f"sample {index + 1} has zero duration in stts", location="stts"
            )
    if tables.ctts is not None:
        composition_offsets = _expand_counts(
            [(e.sample_count, e.sample_offset) for e in tables.ctts], sample_count, "ctts"
        )
    else:
        composition_offsets = [0] * sample_count
    sync_flags = [True] * sample_count
    if tables.sync_samples is not None:
        sync_set = set(tables.sync_samples)
        for sample_number in sync_set:
            if sample_number > sample_count:
                raise IndexValidationError(
                    f"stss references sample {sample_number} but track has "
                    f"{sample_count} samples",
                    location="stss",
                )
        sync_flags = [(i + 1) in sync_set for i in range(sample_count)]
        if not sync_flags[0]:
            raise IndexValidationError(
                "stss does not mark the first sample as a sync sample", location="stss"
            )
    chunk_counts = _chunk_sample_counts(tables)
    if sum(chunk_counts) != sample_count:
        raise IndexValidationError(
            f"chunk map covers {sum(chunk_counts)} samples but tables hold {sample_count}; "
            f"{'missing' if sum(chunk_counts) < sample_count else 'extra'} "
            f"{abs(sample_count - sum(chunk_counts))} sample(s) in stsc/stco",
            location="stsc",
        )
    samples: list[Sample] = []
    cursor = 0
    dts = 0
    min_ctts = 0
    max_ctts = 0
    for chunk_index, (chunk_offset, count) in enumerate(
        zip(tables.chunk_offsets, chunk_counts),
        start=1,
    ):
        position = chunk_offset
        for _ in range(count):
            size = tables.sample_sizes[cursor]
            delta = composition_offsets[cursor]
            min_ctts = min(min_ctts, delta)
            max_ctts = max(max_ctts, delta)
            if abs(delta) > MAX_CTTS_OFFSET:
                raise IndexValidationError(
                    f"sample {cursor + 1} ctts offset {delta} exceeds 32-bit range",
                    location="ctts",
                )
            samples.append(
                Sample(
                    index=cursor,
                    offset=position,
                    size=size,
                    duration=durations[cursor],
                    dts=dts,
                    cts=dts + delta,
                    is_sync=sync_flags[cursor],
                    chunk_index=chunk_index,
                )
            )
            position += size
            dts += durations[cursor]
            cursor += 1
    warnings: list[str] = []
    if min_ctts < 0 or max_ctts > 0:
        warnings.append(
            f"ctts uses composition offsets in [{min_ctts}, {max_ctts}]"
        )
    if tables.duration and dts != tables.duration:
        # A mismatch is an index inconsistency, not a cosmetic warning.
        raise IndexValidationError(
            f"stts sums to {dts} ticks but mdhd declares {tables.duration}; "
            f"delta {dts - tables.duration}",
            location="mdhd",
        )
    sample_map = SampleMap(
        track_id=tables.track_id,
        timescale=tables.timescale,
        samples=samples,
        media_duration=dts,
        mdhd_duration=tables.duration,
        composition_offset_max=max_ctts,
        composition_offset_min=min_ctts,
        warnings=warnings,
    )
    validate_sample_ranges(sample_map, mdat_ranges)
    validate_timeline(sample_map)
    return sample_map


def _range_contains(
    ranges: list[tuple[int, int]], start: int, end: int
) -> bool:
    return any(low <= start and end <= high for low, high in ranges)


def validate_sample_ranges(
    sample_map: SampleMap, mdat_ranges: list[tuple[int, int]]
) -> None:
    previous_end: int | None = None
    seen: set[tuple[int, int]] = set()
    for sample in sample_map.samples:
        if sample.size <= 0:
            raise IndexValidationError(
                f"sample {sample.index + 1} has non-positive size {sample.size}",
                location="stsz/stz2",
            )
        if not _range_contains(mdat_ranges, sample.offset, sample.end):
            raise IndexValidationError(
                f"sample {sample.index + 1} byte range [{sample.offset}, {sample.end}) "
                f"is outside every mdat payload "
                f"({', '.join(f'[{a}, {b})' for a, b in mdat_ranges)})",
                location="stco/co64",
            )
        if previous_end is not None and sample.offset < previous_end:
            raise IndexValidationError(
                f"sample {sample.index + 1} byte range [{sample.offset}, {sample.end}) "
                f"overlaps the previous sample ending at {previous_end}",
                location="stco/co64",
            )
        key = (sample.offset, sample.size)
        if key in seen:
            raise IndexValidationError(
                f"sample {sample.index + 1} repeats byte range "
                f"[{sample.offset}, {sample.end})",
                location="stco/co64",
            )
        seen.add(key)
        previous_end = sample.end


def validate_timeline(sample_map: SampleMap) -> None:
    expected_dts = 0
    for sample in sample_map.samples:
        if sample.dts != expected_dts:
            raise IndexValidationError(
                f"sample {sample.index + 1} DTS {sample.dts} breaks the cumulative "
                f"timeline (expected {expected_dts})",
                location="stts",
            )
        expected_dts += sample.duration
        if sample.cts < 0:
            raise IndexValidationError(
                f"sample {sample.index + 1} has negative composition time {sample.cts}",
                location="ctts",
            )
