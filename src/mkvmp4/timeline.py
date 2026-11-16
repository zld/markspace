"""Per-track timeline normalization from Matroska nanoseconds to MP4 ticks.

Explicit, auditable policy:

1. The track timescale is chosen from the codec/signal: audio tracks use the
   sample rate, video tracks use ``round(fps * 1000)`` (fps from
   DefaultDuration or the median block delta), everything else 1_000_000.
2. A negative first PTS is shifted to zero; the shift is reported.
3. PTS are rounded to integer ticks (half-up). The residual in nanoseconds
   and the number of affected samples are reported, never hidden.
4. Equal PTS values (in any order) are duplicate samples: later decode-order
   occurrences are bumped to the next free tick and counted as corrections.
   A merely smaller PTS from B-frame reordering is left untouched.
5. Backward PTS in decode order is accepted as B-frame reordering. DTS runs
   strictly forward in decode order as the cumulative sum of per-sample
   presentation durations, starting at zero; CTS = PTS - DTS (MP4 ctts v1
   keeps it signed). A non-positive/over-uint32 duration or a composition
   offset outside signed 32-bit range is rejected (``ConversionError``).
6. Each sample's duration is the presentation-order delta starting at its
   PTS; the final sample reuses the previous delta.
"""

from __future__ import annotations

from dataclasses import dataclass, field

import numpy as np

from .errors import ConversionError
from .matroska import Block, Track, TRACK_TYPE_AUDIO, TRACK_TYPE_VIDEO

UINT32_MAX = (1 << 32) - 1
INT32_MAX = (1 << 31) - 1
INT32_MIN = -(1 << 31)
NS_PER_S = 1_000_000_000


@dataclass
class Sample:
    dts: int
    cts_offset: int
    duration: int
    keyframe: bool
    block: Block


@dataclass
class TimelineStats:
    timescale: int
    sample_count: int
    first_pts_ms: float          # original Matroska timeline
    last_pts_ms: float
    rewritten_first_pts_ms: float
    rewritten_last_pts_ms: float
    negative_start_shift_ms: float
    rounding_residual_ns: int
    rounding_affected_samples: int
    duplicate_pts_corrections: int
    reorder_samples: int
    rejected_backward_dts: int = 0


@dataclass
class TrackTimeline:
    track: Track
    stats: TimelineStats
    samples: list[Sample] = field(default_factory=list)


def _video_timescale(track: Track, pts_ns: np.ndarray) -> int:
    if track.default_duration_ns:
        fps = NS_PER_S / track.default_duration_ns
    elif pts_ns.size >= 2:
        deltas = np.diff(np.sort(pts_ns))
        deltas = deltas[deltas > 0]
        fps = (NS_PER_S / np.median(deltas) if deltas.size
               else 25.0)
    else:
        fps = 25.0
    return max(1, int(round(fps * 1000.0)))


def choose_timescale(track: Track, pts_ns: np.ndarray) -> int:
    if track.type == TRACK_TYPE_AUDIO and track.sample_rate > 0:
        return int(round(track.sample_rate))
    if track.type == TRACK_TYPE_VIDEO:
        return _video_timescale(track, pts_ns)
    return 1_000_000


def build_timeline(track: Track, blocks: list[Block]) -> TrackTimeline:
    if not blocks:
        raise ConversionError(f"track {track.number} has no samples")
    pts_ns = np.array([b.pts_ns for b in blocks], dtype=np.int64)
    timescale = choose_timescale(track, pts_ns)

    # 2. negative start -> shift everything to zero.
    shift_ns = int(-pts_ns.min()) if pts_ns.min() < 0 else 0
    shifted = pts_ns + np.int64(shift_ns)

    # 3. half-up rounding, tracking the exact residual.
    exact = shifted.astype(np.float64) * (timescale / NS_PER_S)
    ticks = np.floor(exact + 0.5).astype(np.int64)
    residual_ns = np.abs(
        shifted.astype(np.float64) - (ticks.astype(np.float64)
                                      * (NS_PER_S / timescale))
    ).round().astype(np.int64)
    rounding_affected = int(np.count_nonzero(residual_ns))
    rounding_residual = int(residual_ns.sum())

    # 4. duplicate PTS: bump later decode-order occurrences to the next
    # free tick. Strictly-smaller PTS (B-frames) must stay untouched.
    duplicate_fixes = 0
    used: set[int] = set()
    for i in range(ticks.size):
        value = int(ticks[i])
        while value in used:
            value += 1
            duplicate_fixes += 1
        used.add(value)
        ticks[i] = value

    # 6. presentation-order deltas give each sample a duration; the final
    # sample in presentation order reuses the previous delta.
    order = np.argsort(ticks, kind="stable")
    ordered_pts = ticks[order]
    durations_sorted = np.empty_like(ordered_pts)
    if ordered_pts.size >= 2:
        durations_sorted[:-1] = np.diff(ordered_pts)
        durations_sorted[-1] = durations_sorted[-2]
    else:
        durations_sorted[0] = 1
    durations = np.empty_like(ticks)
    durations[order] = durations_sorted
    if np.any(durations <= 0) or np.any(durations > UINT32_MAX):
        raise ConversionError(
            f"track {track.number}: sample duration outside uint32 range")

    # 5. DTS is the decode-order cumulative sum of the durations, starting
    # at zero; CTS closes the gap back to the (possibly reordered) PTS.
    dts = np.zeros_like(ticks)
    cumulative = 0
    for i in range(ticks.size):
        dts[i] = cumulative
        cumulative += int(durations[i])
    ctts = ticks - dts
    reorder = int(np.count_nonzero(ctts))
    backward = int(np.count_nonzero(np.diff(dts) < 0))
    if backward:
        raise ConversionError(
            f"track {track.number}: {backward} backward decode timestamps "
            "could not be normalized")
    if np.any(ctts > INT32_MAX) or np.any(ctts < INT32_MIN):
        raise ConversionError(
            f"track {track.number}: composition offset outside signed "
            "32-bit range")

    samples = [
        Sample(dts=int(dts[i]), cts_offset=int(ctts[i]),
               duration=int(durations[i]),
               keyframe=blocks[i].keyframe, block=blocks[i])
        for i in range(len(blocks))
    ]
    first_ms = float(pts_ns[0]) / 1_000_000.0
    last_ms = float(pts_ns[-1]) / 1_000_000.0
    stats = TimelineStats(
        timescale=timescale,
        sample_count=len(samples),
        first_pts_ms=first_ms,
        last_pts_ms=last_ms,
        rewritten_first_pts_ms=float(ticks.min()) / timescale * 1000.0,
        rewritten_last_pts_ms=float(ticks.max()) / timescale * 1000.0,
        negative_start_shift_ms=shift_ns / 1_000_000.0,
        rounding_residual_ns=rounding_residual,
        rounding_affected_samples=rounding_affected,
        duplicate_pts_corrections=duplicate_fixes,
        reorder_samples=reorder,
        rejected_backward_dts=backward,
    )
    return TrackTimeline(track=track, stats=stats, samples=samples)


def build_all(tracks: list[Track], clusters) -> list[TrackTimeline]:
    """Group cluster blocks per selected track (preserving decode order).

    Blocks belonging to tracks outside the selection are ignored; every
    selected track is guaranteed an entry even if it later fails for having
    no samples (which build_timeline reports explicitly).
    """
    wanted = {t.number for t in tracks}
    per_track: dict[int, list[Block]] = {t.number: [] for t in tracks}
    for cluster in clusters:
        for block in cluster.blocks:
            if block.track_number in wanted:
                per_track[block.track_number].append(block)
    return [build_timeline(t, per_track[t.number]) for t in tracks]
