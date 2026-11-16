"""Build per-track fragment boundaries under byte and duration budgets."""

from __future__ import annotations

from .model import Boundary, MappingError, Track


def build_boundaries(
    tracks: list[Track],
    max_bytes: int,
    max_duration: int | None = None,
) -> list[Boundary]:
    if max_bytes <= 0:
        raise MappingError("max-bytes must be positive")
    if max_duration is not None and max_duration <= 0:
        raise MappingError("max-duration must be positive")
    boundaries: list[Boundary] = []
    for track in tracks:
        boundaries.extend(_build_for_track(track, max_bytes, max_duration))
    return boundaries


def _build_for_track(
    track: Track, max_bytes: int, max_duration: int | None
) -> list[Boundary]:
    samples = track.samples
    for sample in samples:
        if sample.size > max_bytes:
            raise MappingError(
                f"track {track.track_id} sample {sample.index} size {sample.size} "
                f"exceeds max-bytes {max_bytes}"
            )
    boundaries: list[Boundary] = []
    start = 0
    boundary_index = 0
    if track.has_stss and not samples[0].is_sync:
        raise MappingError(
            f"track {track.track_id} first sample is not a sync sample; "
            "video boundaries cannot start safely"
        )
    while start < len(samples):
        end = _extend_group(track, samples, start, max_bytes, max_duration)
        group = samples[start:end]
        first = group[0]
        last = group[-1]
        group_duration = sum(s.duration for s in group)
        boundaries.append(
            Boundary(
                track_id=track.track_id,
                index=boundary_index,
                offset=first.offset,
                length=last.end - first.offset,
                start_time=first.dts,
                end_time=first.dts + group_duration,
                sample_first=first.index,
                sample_last=last.index,
                starts_with_sync=first.is_sync,
            )
        )
        boundary_index += 1
        start = end
    return boundaries


def _extend_group(
    track: Track,
    samples: list,
    start: int,
    max_bytes: int,
    max_duration: int | None,
) -> int:
    first = samples[start]
    end = start + 1
    while end < len(samples):
        candidate = samples[end]
        # Video boundaries must start on a sync sample, so stop before one.
        if track.has_stss and candidate.is_sync and end > start:
            break
        group_bytes = candidate.end - first.offset
        if group_bytes > max_bytes:
            break
        if max_duration is not None:
            group_duration = candidate.dts + candidate.duration - first.dts
            if group_duration > max_duration:
                break
        end += 1
    return end
