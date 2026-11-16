"""Validate sample ranges, timestamps and declared sidx offsets."""

from __future__ import annotations

from .model import MappingError, MdatExtent, SidxIndex, Track


def validate_tracks(tracks: list[Track], mdats: list[MdatExtent]) -> list[str]:
    """Run semantic checks. Returns human-readable warnings; raises on error."""
    if not tracks:
        raise MappingError("file declares no usable tracks")
    if not mdats:
        raise MappingError("file has no mdat payload")
    warnings: list[str] = []
    for track in tracks:
        _validate_sample_ranges(track, mdats)
        _validate_timing(track, warnings)
    return warnings


def _validate_sample_ranges(track: Track, mdats: list[MdatExtent]) -> None:
    ordered = sorted(track.samples, key=lambda s: s.offset)
    previous_end = -1
    for sample in ordered:
        if sample.size <= 0:
            raise MappingError(
                f"track {track.track_id} sample {sample.index} has non-positive size"
            )
        if not any(
            sample.offset >= m.offset and sample.end <= m.end for m in mdats
        ):
            raise MappingError(
                f"track {track.track_id} sample {sample.index} "
                f"[{sample.offset},{sample.end}) outside mdat"
            )
        if sample.offset < previous_end:
            raise MappingError(
                f"track {track.track_id} sample ranges overlap near {sample.offset}"
            )
        previous_end = sample.end


def _validate_timing(track: Track, warnings: list[str]) -> None:
    samples = track.samples
    if not samples:
        raise MappingError(f"track {track.track_id} has no samples")
    for sample in samples:
        if sample.duration <= 0:
            warnings.append(
                f"track {track.track_id} sample {sample.index} has zero/negative "
                "duration (timestamp gap)"
            )
            break
    for prev, cur in zip(samples, samples[1:]):
        if cur.dts < prev.dts:
            raise MappingError(
                f"track {track.track_id} decode time not monotonic at sample {cur.index}"
            )
        if cur.dts != prev.dts + prev.duration:
            warnings.append(
                f"track {track.track_id} has timestamp gap before sample {cur.index}"
            )
            break
    pts = [s.pts for s in samples]
    if pts != sorted(pts):
        raise MappingError(
            f"track {track.track_id} presentation time not monotonic"
        )
    if track.has_stss and not any(s.is_sync for s in samples):
        raise MappingError(
            f"track {track.track_id} declares stss but lists no sync samples"
        )


def validate_sidx(index: SidxIndex | None) -> None:
    """Structural checks live in sidx.parse_sidx; this guards empty tables."""
    if index is not None and not index.references:
        raise MappingError("sidx box declares zero references")
