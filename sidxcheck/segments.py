"""Segment planning at sync-sample boundaries."""

from __future__ import annotations

from dataclasses import dataclass, field

from .errors import IndexValidationError, UnsupportedFeatureError
from .samplemap import SampleMap

MAX_SEGMENTS = 65_535  # one SIDX box can express at most 65535 references


@dataclass(frozen=True)
class Segment:
    index: int
    sample_start: int
    sample_end: int
    dts_start: int
    duration_ticks: int
    byte_start: int
    byte_end: int
    starts_with_sap: bool = True
    sap_type: int = 1
    sap_delta_time: int = 0

    @property
    def sample_count(self) -> int:
        return self.sample_end - self.sample_start

    @property
    def byte_size(self) -> int:
        return self.byte_end - self.byte_start


@dataclass
class SegmentPlan:
    timescale: int
    target_ticks: int
    max_drift_ticks: int
    segments: list[Segment]
    warnings: list[str] = field(default_factory=list)

    @property
    def total_duration(self) -> int:
        return sum(segment.duration_ticks for segment in self.segments)


def _ensure_contiguous(sample_map: SampleMap) -> None:
    previous = None
    for sample in sample_map.samples:
        if previous is not None and sample.offset != previous.end:
            raise UnsupportedFeatureError(
                f"sample {sample.index + 1} starts at {sample.offset} but the previous "
                f"sample ends at {previous.end}; interleaved or gapped sample data "
                f"cannot be expressed as contiguous SIDX references",
                location="stco/co64",
            )
        previous = sample


def plan_segments(
    sample_map: SampleMap,
    *,
    target_ticks: int,
    max_drift_ticks: int = 0,
) -> SegmentPlan:
    if target_ticks <= 0:
        raise ValueError("target segment duration must be positive")
    if max_drift_ticks < 0:
        raise ValueError("max drift must be non-negative")
    if not sample_map.samples:
        raise IndexValidationError("cannot plan segments for an empty track", location="stbl")
    _ensure_contiguous(sample_map)
    samples = sample_map.samples
    total_ticks = sample_map.media_duration
    sync_positions = sample_map.sync_indices
    sync_dts = {position: samples[position].dts for position in sync_positions}
    segments: list[Segment] = []
    cursor = 0
    while cursor < len(samples):
        target = samples[cursor].dts + target_ticks
        if target >= total_ticks:
            end = len(samples)
        else:
            candidates = [
                position
                for position in sync_positions
                if position > cursor and samples[position].dts <= total_ticks
            ]
            best: int | None = None
            best_delta: int | None = None
            for position in candidates:
                delta = sync_dts[position] - target
                if abs(delta) > max_drift_ticks:
                    continue
                if best_delta is None or (abs(delta), delta) < (
                    abs(best_delta),
                    best_delta,
                ):
                    best = position
                    best_delta = delta
            if best is None:
                nearest = min(
                    candidates,
                    key=lambda p: abs(sync_dts[p] - target),
                    default=None,
                )
                if nearest is None:
                    raise IndexValidationError(
                        f"segment starting at sample {cursor + 1} (dts {samples[cursor].dts}) "
                        f"has no sync sample after it to close a {target_ticks}-tick segment",
                        location="stss",
                    )
                raise IndexValidationError(
                    f"segment starting at sample {cursor + 1} (dts {samples[cursor].dts}) "
                    f"targets dts {target}, but the nearest sync sample is {nearest + 1} "
                    f"at dts {sync_dts[nearest]} (drift {sync_dts[nearest] - target}, "
                    f"allowed ±{max_drift_ticks})",
                    location="stss",
                )
            end = best
        if end <= cursor:
            raise IndexValidationError(
                f"segment boundary at sample {end + 1} does not advance past {cursor + 1}",
                location="stss",
            )
        first = samples[cursor]
        last = samples[end - 1]
        if not first.is_sync:
            raise IndexValidationError(
                f"segment starting at sample {cursor + 1} is not a sync sample",
                location="stss",
            )
        segments.append(
            Segment(
                index=len(segments),
                sample_start=cursor,
                sample_end=end,
                dts_start=first.dts,
                duration_ticks=sum(s.duration for s in samples[cursor:end]),
                byte_start=first.offset,
                byte_end=last.end,
            )
        )
        if len(segments) > MAX_SEGMENTS:
            raise IndexValidationError(
                f"segment plan exceeds {MAX_SEGMENTS} references, which one SIDX "
                f"cannot express",
                location="sidx",
            )
        cursor = end
    warnings: list[str] = []
    if len(segments) > 1:
        actual = [s.duration_ticks for s in segments[:-1]]
        if len(set(actual)) > 1:
            warnings.append(
                "segment durations vary to stay on sync boundaries: "
                + ", ".join(str(value) for value in actual)
            )
    return SegmentPlan(
        timescale=sample_map.timescale,
        target_ticks=target_ticks,
        max_drift_ticks=max_drift_ticks,
        segments=segments,
        warnings=warnings,
    )
