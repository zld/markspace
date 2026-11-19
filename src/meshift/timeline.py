"""Timestamp normalization and output timebase conversion."""

from __future__ import annotations

from dataclasses import dataclass, field

import numpy as np

from .errors import TimelineError
from .matroska import MatroskaFile, RawBlock, Track

#: MP4 media timescale: one tick is exactly one nanosecond.
OUTPUT_TIMESCALE = 1_000_000_000


def half_div(value: int, divisor: int) -> int:
    """Integer division rounded half away from zero."""
    if divisor <= 0:
        raise TimelineError(f"non-positive divisor {divisor}")
    sign = -1 if value < 0 else 1
    return sign * ((abs(value) * 2 + divisor) // (2 * divisor))


@dataclass
class NormalizedSample:
    dts_ns: int
    pts_ns: int
    composition_offset_ns: int
    duration_ns: int
    payload: bytes
    is_keyframe: bool
    source: str
    location: str


@dataclass
class NormalizedTrack:
    input_number: int
    output_number: int
    kind: int
    codec_id: str
    timescale: int
    samples: list[NormalizedSample] = field(default_factory=list)
    warnings: list[str] = field(default_factory=list)

    def dts_array(self) -> np.ndarray:
        return np.array([s.dts_ns for s in self.samples], dtype=np.int64)

    def pts_array(self) -> np.ndarray:
        return np.array([s.pts_ns for s in self.samples], dtype=np.int64)

    def monotonic_ok(self) -> bool:
        dts = self.dts_array()
        pts = self.pts_array()
        return bool(dts.size < 2 or (np.all(np.diff(dts) > 0)
                                     and np.all(np.diff(pts) >= 0)))

    def interval_stats(self) -> dict[str, float]:
        dts = self.dts_array()
        if dts.size < 2:
            return {"min_ns": 0.0, "max_ns": 0.0, "mean_ns": 0.0}
        gaps = np.diff(dts).astype(np.float64)
        return {"min_ns": float(gaps.min()), "max_ns": float(gaps.max()),
                "mean_ns": float(gaps.mean())}

    @property
    def first_pts_ns(self) -> int:
        return self.samples[0].pts_ns if self.samples else 0

    @property
    def last_pts_ns(self) -> int:
        return self.samples[-1].pts_ns if self.samples else 0

    @property
    def first_dts_ns(self) -> int:
        return self.samples[0].dts_ns if self.samples else 0

    @property
    def last_dts_ns(self) -> int:
        return self.samples[-1].dts_ns if self.samples else 0


def _location(block: RawBlock) -> str:
    return f"cluster[{block.cluster_index}].block[{block.block_index}]"


def _raw_dts_ns(block: RawBlock, scale: int) -> int | None:
    if block.cluster_timecode is None:
        return None
    return (block.cluster_timecode + block.relative_timecode) * scale


def _fill_missing(blocks: list[RawBlock], scale: int) -> list[int | None]:
    dts: list[int | None] = []
    last_known: int | None = None
    gap = 0
    for block in blocks:
        value = _raw_dts_ns(block, scale)
        if value is None:
            gap += 1
            dts.append(None)
            continue
        if last_known is not None and gap:
            step = (value - last_known) // (gap + 1)
            if step <= 0:
                raise TimelineError(
                    "untimestamped samples cannot be placed without "
                    f"overlapping; near {_location(block)}")
            for idx in range(1, gap + 1):
                dts[-idx] = value - step * (gap - idx + 1)
        last_known, gap = value, 0
        dts.append(value)
    if gap:         # Trailing samples have no following anchor: flat fill.
        for idx in range(1, gap + 1):
            dts[-idx] = (last_known or 0)
    return dts


def normalize(mkv: MatroskaFile,
              output_numbers: dict[int, int] | None = None
              ) -> list[NormalizedTrack]:
    """Normalize every track onto a zero-origin integer-nanosecond axis."""
    output_numbers = output_numbers or {}
    tracks: list[NormalizedTrack] = []
    for order, track in enumerate(mkv.tracks, start=1):
        out_no = output_numbers.get(track.number, order)
        tracks.append(NormalizedTrack(
            track.number, out_no, track.kind, track.codec_id,
            OUTPUT_TIMESCALE))

    for normalized, track in zip(tracks, mkv.tracks):
        blocks = [b for b in mkv.blocks if b.track_number == track.number]
        if not blocks:
            normalized.warnings.append("empty track: no samples present")
            continue
        raw_dts = _fill_missing(blocks, mkv.timecode_scale)

        base_dts = next(v for v in raw_dts if v is not None)
        previous: int | None = None
        for block, dts_raw in zip(blocks, raw_dts):
            assert dts_raw is not None
            dts = dts_raw - base_dts
            if previous is not None and dts <= previous:
                normalized.warnings.append(
                    f"non-increasing DTS at {_location(block)}; forced to "
                    "strictly increasing")
                dts = previous + 1
            composition = half_div(
                block.composition_offset * mkv.timecode_scale, 1)
            normalized.samples.append(NormalizedSample(
                dts_ns=dts,
                pts_ns=dts + composition,
                composition_offset_ns=composition,
                duration_ns=track.default_duration_ns or 0,
                payload=block.payload,
                is_keyframe=block.is_keyframe,
                source=block.source, location=_location(block)))
            previous = dts

        pts_shift = min(s.pts_ns for s in normalized.samples)
        if pts_shift != 0:
            # DTS starts at zero by construction; the PTS-DTS composition
            # offset is preserved verbatim (negative offsets included).
            normalized.warnings.append(
                f"track first PTS is {pts_shift} ns relative to first DTS; "
                "signed composition offset retained")

        _derive_durations(normalized, track)
        if not normalized.monotonic_ok():
            raise TimelineError(
                f"track {track.number} failed monotonicity after rewrite")
    return tracks


def _derive_durations(normalized: NormalizedTrack, track: Track) -> None:
    samples = normalized.samples
    dts = normalized.dts_array()
    if samples:
        gaps = np.diff(dts)
        for idx, sample in enumerate(samples[:-1]):
            if sample.duration_ns <= 0:
                sample.duration_ns = int(gaps[idx])
        if samples[-1].duration_ns <= 0:
            samples[-1].duration_ns = (int(gaps[-1])
                                       if gaps.size else 0)
    if any(s.duration_ns < 0 for s in samples):
        raise TimelineError(
            f"track {track.number} produced a negative sample duration")
