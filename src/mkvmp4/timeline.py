"""Timeline reconstruction: DTS/PTS, rebasing and time-base conversion.

Matroska blocks carry presentation timestamps (PTS) in TimestampScale ticks
and are stored in decode order. MP4 sample tables need:

* a monotonic per-track DTS sequence;
* per-sample composition offsets ``pts - dts``;
* integer movie-timescale durations.

The whole file is rebased so the earliest PTS across *all* tracks becomes
zero: inter-track offsets and silent gaps are preserved, only the global
start is shifted. NumPy performs the bulk int64 arithmetic and the drift
check that catches repeated rounding errors against the float reference.
"""

from dataclasses import dataclass

import numpy as np

from .errors import CorruptMKVError
from .mkv.blocks import Sample
from .mkv.segment import MatroskaFile, TrackInfo

#: Movie timescale used for every MP4 output (millisecond precision).
MOVIE_TIMESCALE = 1000
#: A conversion may drift at most this many milliseconds vs. the float path.
MAX_ROUND_DRIFT_MS = 1.0


@dataclass(slots=True)
class TimedSample:
    """One sample after timeline construction."""

    track_number: int
    dts_ms: int
    pts_ms: int
    duration_ms: int
    keyframe: bool
    data: bytes

    @property
    def composition_offset_ms(self) -> int:
        return self.pts_ms - self.dts_ms


@dataclass(slots=True)
class TrackTimeline:
    """Per-track samples in decode order plus timing metadata."""

    track: TrackInfo
    samples: list[TimedSample]
    first_pts_ms: int
    last_pts_ms: int


@dataclass(slots=True)
class Timeline:
    """All track timelines plus the global rebase offset."""

    scale_ns: int
    rebase_ticks: int
    rebase_ms: float
    tracks: list[TrackTimeline]

    def track_numbers(self) -> list[int]:
        return [t.track.number for t in self.tracks]


def _build_dts_sequence(pts: np.ndarray) -> np.ndarray:
    """Derive a strictly monotonic DTS array from decode-ordered PTS.

    Matroska stores blocks in decode order while their PTS follows display
    order (B-frames make the two differ). The frame cadence is taken from the
    smallest positive gap between *sorted* presentation times; decode slot
    ``i`` then gets DTS ``first_display_pts + i*cadence - lead`` where the
    lead shifts the whole sequence so every composition offset
    ``pts - dts`` stays non-negative.
    """
    if pts.size == 0:
        return pts.copy()
    if pts.size == 1:
        return pts.copy()
    ordered = np.sort(pts)
    gaps = np.diff(ordered)
    positive = gaps[gaps > 0]
    if positive.size == 0:
        # Degenerate input (all PTS equal): fall back to a unit cadence.
        step = 1
    else:
        step = int(np.min(positive))
    first_display = int(ordered[0])
    # Shift the decode sequence down by the largest overshoot so every
    # composition offset pts - dts stays non-negative.
    max_overshoot = int(np.max(
        first_display + step * np.arange(pts.size) - pts))
    first_dts = first_display - max_overshoot
    return first_dts + step * np.arange(pts.size, dtype=np.int64)


def _sample_durations(dts_ms: np.ndarray) -> np.ndarray:
    """Per-sample durations from the monotonic DTS cadence.

    PTS diffs are unusable for duration when B-frames reorder presentation,
    because they can be negative. The decode timeline always advances, so the
    gap to the next DTS is the correct sample duration; the last sample
    repeats the previous cadence.
    """
    if dts_ms.size == 0:
        return np.zeros(0, dtype=np.int64)
    if dts_ms.size == 1:
        return np.array([0], dtype=np.int64)
    durations = np.diff(dts_ms)
    if np.any(durations <= 0):
        raise CorruptMKVError("non-positive sample duration derived from DTS")
    return np.append(durations, durations[-1])


def build_timeline(mkv: MatroskaFile, samples: list[Sample]) -> Timeline:
    """Construct per-track DTS/PTS timelines rebased to the earliest time.

    The rebase origin is the earliest DTS across every track, so MP4 output
    never carries negative decode times; the earliest *presentation* still
    defines the reported ``rebase_ms`` and may stay above zero for video
    that has to decode B-frames ahead of its first displayed frame.
    """
    if not samples:
        raise CorruptMKVError("input contains no media samples")

    scale_ns = mkv.timestamp_scale
    by_track: dict[int, list[Sample]] = {t.number: [] for t in mkv.tracks}
    for sample in samples:
        by_track[sample.track_number].append(sample)

    # Pass 1: derive raw (un-rebased) tick arrays per track.
    raw: dict[int, tuple[np.ndarray, np.ndarray]] = {}
    for track in mkv.tracks:
        track_samples = by_track.get(track.number, [])
        if not track_samples:
            continue
        pts_ticks = np.array([s.pts_ticks for s in track_samples],
                             dtype=np.int64)
        # Audio and other non-video tracks have no reordering: their DTS is
        # their PTS. Video may contain B-frames and needs a derived cadence.
        if track.track_type == 1:
            dts_ticks = _build_dts_sequence(pts_ticks)
        else:
            dts_ticks = pts_ticks.copy()
        if np.any(np.diff(dts_ticks) <= 0):
            raise CorruptMKVError(
                f"track {track.number} DTS is not strictly monotonic")
        raw[track.number] = (dts_ticks, pts_ticks)

    global_first_dts = min(int(dts[0]) for dts, _ in raw.values())
    global_first_pts = min(s.pts_ticks for s in samples)

    timelines: list[TrackTimeline] = []
    for track in mkv.tracks:
        if track.number not in raw:
            continue
        dts_ticks, pts_ticks = raw[track.number]
        dts_ms = _ticks_to_ms(dts_ticks - global_first_dts, scale_ns)
        pts_ms = _ticks_to_ms(pts_ticks - global_first_dts, scale_ns)
        if np.any(pts_ms < dts_ms):
            raise CorruptMKVError(
                f"track {track.number} has PTS before DTS after rebasing")
        if np.any(dts_ms < 0) or np.any(pts_ms < 0):
            raise CorruptMKVError(
                f"track {track.number} has negative timestamps after rebasing")
        durations_ms = _sample_durations(dts_ms)
        track_samples = by_track[track.number]
        timed = [
            TimedSample(
                track_number=track.number,
                dts_ms=int(dts_ms[i]),
                pts_ms=int(pts_ms[i]),
                duration_ms=int(durations_ms[i]),
                keyframe=track_samples[i].keyframe,
                data=track_samples[i].data,
            )
            for i in range(len(track_samples))
        ]
        timelines.append(TrackTimeline(
            track=track,
            samples=timed,
            first_pts_ms=int(pts_ms[0]),
            last_pts_ms=int(pts_ms[-1]),
        ))

    # Reported rebase value is the earliest *presentation* time removed from
    # the source timeline (the value a user sees in the terminal report).
    rebase_ms = global_first_pts * scale_ns / 1_000_000.0
    return Timeline(
        scale_ns=scale_ns,
        rebase_ticks=global_first_pts,
        rebase_ms=rebase_ms,
        tracks=timelines,
    )


def _ticks_to_ms(ticks: np.ndarray, scale_ns: int) -> np.ndarray:
    """Convert TimestampScale ticks to integer movie-timescale units.

    The integer rounded result is checked against the float reference; an
    accumulated deviation beyond :data:`MAX_ROUND_DRIFT_MS` means the chosen
    timescale cannot represent the source timeline accurately enough.
    """
    reference = ticks.astype(np.float64) * (scale_ns / 1_000_000.0)
    rounded = np.rint(reference).astype(np.int64)
    drift = np.abs(reference - rounded.astype(np.float64))
    if np.any(drift > MAX_ROUND_DRIFT_MS):
        worst = float(np.max(drift))
        raise CorruptMKVError(
            f"timestamp rounding drift {worst:.3f} ms exceeds "
            f"{MAX_ROUND_DRIFT_MS} ms at timescale {MOVIE_TIMESCALE}")
    return rounded
