"""Turn expanded sample tables into timed, byte-addressed segments.

All time math uses :class:`Fraction` against each track's own timescale, so
segment boundaries stay exact regardless of how the media timescale relates to
the millisecond target.  Video segments open on sync samples; audio samples are
assigned by decode time; boundary drift beyond the configured tolerance is a
hard failure (never silently snapped).
"""

from __future__ import annotations

from dataclasses import dataclass
from fractions import Fraction

from .errors import FormatError, ValidationError
from .sample_tables import AUDIO_HANDLER, VIDEO_HANDLER, SampleTable


@dataclass
class Segment:
    index: int
    start_ms: int
    end_ms: int
    # Per track: ordered 0-based [sample_start, sample_end) indices.
    track_samples: dict[int, tuple[int, int]]
    start_time_sidx: int  # boundary in the chosen SIDX timescale
    end_time_sidx: int


@dataclass
class Layout:
    segments: list[Segment]
    sidx_timescale: int
    video_track_id: int
    drift_ms: Fraction


def _ms_units(timescale: int) -> Fraction:
    return Fraction(timescale, 1000)


def _video_boundary_samples(video: SampleTable, segment_ms: int, target_count: int) -> list[tuple[int, Fraction]]:
    """Sync sample indices opening each segment, plus the end sentinel.

    Returns ``(sample_index, boundary_time_ms)`` pairs where the final pair is
    ``(count, duration_ms)``.
    """
    if video.handler != VIDEO_HANDLER:
        raise FormatError("segment planning requires a video track")
    sync = [i for i in range(video.count) if video.is_sync(i)]
    if not sync:
        raise FormatError(f"video track {video.track_id} has no sync samples")
    unit = Fraction(1000, video.timescale)  # ms per timescale tick
    dts_ms = [Fraction(d, 1) * unit for d in video.dts]
    duration_ms = Fraction(video.duration, 1) * unit
    boundaries: list[tuple[int, Fraction]] = []
    for segment_index in range(target_count + 1):
        target = Fraction(segment_ms * segment_index, 1)
        if segment_index == 0:
            boundaries.append((sync[0], Fraction(0)))
        elif segment_index == target_count:
            # Final sentinel: one past the last sample at the true duration.
            boundaries.append((video.count, duration_ms))
        else:
            # Nearest sync sample at or before the target; segments open on SAP.
            candidate = sync[0]
            for sample_index in sync:
                if dts_ms[sample_index] <= target:
                    candidate = sample_index
                else:
                    break
            boundaries.append((candidate, dts_ms[candidate]))
    # Deduplicate zero-length opening (first sync must be sample 0).
    if boundaries[0][0] != 0:
        raise FormatError("first video sync sample is not sample 0; cannot anchor timeline")
    cleaned: list[tuple[int, Fraction]] = []
    for pair in boundaries:
        if cleaned and pair[0] == cleaned[-1][0]:
            continue
        cleaned.append(pair)
    return cleaned


def plan_segments(tables: list[SampleTable], segment_ms: int, max_drift_ms: int) -> Layout:
    videos = [t for t in tables if t.handler == VIDEO_HANDLER]
    if len(videos) != 1:
        raise FormatError(f"expected exactly one video track, found {len(videos)}")
    video = videos[0]
    if segment_ms <= 0 or max_drift_ms < 0:
        raise FormatError("segment-ms must be positive and max-drift-ms non-negative")
    unit_ms = Fraction(1000, video.timescale)
    # Full stts timeline: last DTS plus the final sample's own delta.  This is
    # the authoritative media duration and must agree with mdhd at parse time.
    last_delta = video.dts[-1] - video.dts[-2] if video.count > 1 else (video.duration - video.dts[-1])
    duration_ms = Fraction(video.dts[-1] + last_delta, 1) * unit_ms
    target_count = round(duration_ms / segment_ms)
    if target_count <= 0:
        raise FormatError("media shorter than one target segment")
    boundary_pairs = _video_boundary_samples(video, segment_ms, target_count)
    tolerance = Fraction(max_drift_ms, 1)
    drift_max = Fraction(0)
    # Compare every boundary after t=0 against its ideal grid position.
    for position, (sample_index, actual_ms) in enumerate(boundary_pairs[1:], start=1):
        ideal = Fraction(segment_ms * position, 1)
        drift_max = max(drift_max, abs(actual_ms - ideal))
    if drift_max > tolerance:
        raise ValidationError(
            f"segment boundary drift {float(drift_max):.2f}ms exceeds tolerance {max_drift_ms}ms"
        )
    segments: list[Segment] = []
    for position, (start_sample, start_ms) in enumerate(boundary_pairs[:-1]):
        end_sample, end_ms = boundary_pairs[position + 1]
        if end_sample <= start_sample:
            raise FormatError("empty video segment produced by boundary planning")
        track_samples: dict[int, tuple[int, int]] = {video.track_id: (start_sample, end_sample)}
        for table in tables:
            if table.handler == VIDEO_HANDLER:
                continue
            if table.handler != AUDIO_HANDLER:
                raise FormatError(f"unsupported handler type {table.handler!r} on track {table.track_id}")
            a_unit = Fraction(1000, table.timescale)
            lo = next((i for i in range(table.count) if Fraction(table.dts[i], 1) * a_unit >= start_ms), table.count)
            hi = next((i for i in range(table.count) if Fraction(table.dts[i], 1) * a_unit >= end_ms), table.count)
            track_samples[table.track_id] = (lo, hi)
        segments.append(
            Segment(
                index=position,
                start_ms=int(start_ms),
                end_ms=int(end_ms),
                track_samples=track_samples,
                start_time_sidx=_to_timescale(start_ms, video.timescale),
                end_time_sidx=_to_timescale(end_ms, video.timescale),
            )
        )
    # Audio samples must cover the whole timeline with no gaps or leftovers.
    for table in tables:
        if table.handler != AUDIO_HANDLER:
            continue
        covered = sum(hi - lo for lo, hi in (s.track_samples[table.track_id] for s in segments))
        if covered != table.count:
            raise FormatError(
                f"audio track {table.track_id}: {covered}/{table.count} samples assigned to segments"
            )
    return Layout(segments, video.timescale, video.track_id, drift_max)


def _to_timescale(ms_value: Fraction, timescale: int) -> int:
    value = ms_value * timescale / 1000
    if value.denominator != 1:
        raise FormatError(f"segment boundary {ms_value}ms is not integral in timescale {timescale}")
    return int(value)


def collect_sample_bytes(table: SampleTable, start: int, end: int) -> list[tuple[int, int]]:
    """Absolute source-file [offset, length) read list for samples [start, end)."""
    if not (0 <= start <= end <= table.count):
        raise FormatError(f"sample slice [{start},{end}) out of range for track {table.track_id}")
    return [(table._sample_offsets[i], table.sizes[i]) for i in range(start, end)]
