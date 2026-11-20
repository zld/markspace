"""Shared timeline: PTS extraction, duration resolution, compact sample index.

Samples are held as NumPy int64 arrays (never one Python object per frame) so a
five-million-sample input keeps only a compact index in memory.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np

from .matroska import ParsedMatroska, Track, TrackSamples

INT64_MIN = -(1 << 63)
INT64_MAX = (1 << 63) - 1
INT32_MIN = -(1 << 31)
INT32_MAX = (1 << 31) - 1
UINT32_MAX = (1 << 32) - 1
NS_PER_S = 1_000_000_000


class TimelineError(Exception):
    """Unsupported or invalid timeline (reordering, unknown last duration...)."""


class TimebaseOverflow(TimelineError):
    """A timestamp or duration does not fit in signed 64-bit arithmetic."""


def ticks_to_ns(ticks: np.ndarray | int, scale_ns: int) -> np.ndarray | int:
    """Multiply tick counts by a positive scale, checking int64 bounds."""

    if scale_ns <= 0:
        raise TimelineError("TimestampScale must be positive")
    array = np.asarray(ticks, dtype=np.int64)
    # Floor division would let a small negative value through; compare against
    # the exact representable range instead.
    if np.any(array > INT64_MAX // scale_ns) or np.any(array < -(INT64_MAX // scale_ns)):
        raise TimebaseOverflow("timestamp * TimestampScale overflows int64")
    result = array * np.int64(scale_ns)
    if result.size and (result.min() < INT64_MIN or result.max() > INT64_MAX):
        raise TimebaseOverflow("timestamp * TimestampScale overflows int64")
    return int(result) if np.isscalar(ticks) else result


def ns_to_ticks(ns: np.ndarray | int, timescale: int) -> np.ndarray | int:
    """Round nanoseconds to target ticks (half away from zero), int64-safe."""

    if timescale <= 0:
        raise TimelineError("MP4 timescale must be positive")
    array = np.asarray(ns, dtype=np.int64)
    # ``ns * timescale`` must fit; rounding afterwards can only add one tick.
    if (np.any(array > (INT64_MAX - 1) // timescale) or
            np.any(array < -(INT64_MAX - 1) // timescale)):
        raise TimebaseOverflow("timestamp * timescale overflows int64")
    product = array * np.int64(timescale)
    negative = product < 0
    magnitude = np.abs(product)
    quotient = magnitude // np.int64(NS_PER_S)
    remainder = magnitude % np.int64(NS_PER_S)
    rounded = quotient + (remainder >= np.int64(NS_PER_S // 2))
    result = np.where(negative, -rounded, rounded)
    if result.size and (result.min() < INT64_MIN or result.max() > INT64_MAX):
        raise TimebaseOverflow("rescaled timestamp overflows int64")
    return int(result) if np.isscalar(ns) else result


def ticks_to_ms(ticks: int, timescale: int) -> int:
    if timescale <= 0:
        raise TimelineError("timescale must be positive")
    return int(ns_to_ticks(ticks * NS_PER_S // timescale, 1000))


@dataclass(slots=True)
class TrackTimeline:
    track: Track
    mp4_track_id: int
    pts: np.ndarray        # presentation time on the global timeline (ticks)
    media_pts: np.ndarray  # presentation time inside this track (ticks)
    dts: np.ndarray        # decode time inside this track (ticks)
    ctts: np.ndarray       # signed composition offset (media_pts - dts)
    duration: np.ndarray   # sample duration (ticks)
    offset: np.ndarray     # byte offset of the compressed frame in the source
    size: np.ndarray       # compressed frame size
    keyframe: np.ndarray
    media_base: int        # global PTS of the first presented sample
    first_pts: int         # PTS of the first sample in decode order
    last_pts: int          # PTS of the last sample in decode order
    max_pts: int
    duration_ticks: int    # media duration, covering the last presented frame

    @property
    def sample_count(self) -> int:
        return int(self.pts.shape[0])

    @property
    def has_ctts(self) -> bool:
        return bool(np.any(self.ctts != 0))


@dataclass(slots=True)
class Timeline:
    timescale: int
    mode: str
    tracks: list[TrackTimeline]
    shift_ns: int
    global_first_pts: int
    global_last_pts: int
    sample_count: int
    ctts_tracks: int
    source_first_pts: int = 0
    normalized: bool = False

    def track(self, mkv_number: int) -> TrackTimeline:
        for item in self.tracks:
            if item.track.number == mkv_number:
                return item
        raise KeyError(mkv_number)


def _collect(mkv: ParsedMatroska) -> dict[int, TrackSamples]:
    if mkv.timestamp_scale_ns <= 0:
        raise TimelineError("TimestampScale must be positive")
    return {track.number: mkv.samples[track.number] for track in mkv.tracks}


def _resolve_durations(pts: np.ndarray, known: np.ndarray,
                       default: np.ndarray, track: Track) -> np.ndarray:
    """Resolve each sample duration: BlockDuration, DefaultDuration, interval.

    Vectorised: the only cross-sample dependency is the following presentation
    interval, which is a shifted difference over the PTS sort order.
    """

    duration = known.copy()
    unresolved = duration <= 0
    if not np.any(unresolved):
        return duration
    use_default = unresolved & (default > 0)
    duration[use_default] = default[use_default]
    unresolved &= ~use_default
    if not np.any(unresolved):
        return duration
    order = np.argsort(pts, kind="stable")
    ordered_pts = pts[order]
    interval = np.zeros_like(pts)
    if ordered_pts.size > 1:
        interval[order[:-1]] = ordered_pts[1:] - ordered_pts[:-1]
    usable = unresolved & (interval > 0)
    duration[usable] = interval[usable]
    unresolved &= ~usable
    if np.any(unresolved):
        index = int(np.flatnonzero(unresolved)[0])
        final = index == int(order[-1])
        kind = "final" if final else "sample"
        raise TimelineError(
            f"track {track.number}: cannot determine duration of {kind} at PTS "
            f"{int(pts[index])} ns (no BlockDuration, DefaultDuration, or "
            f"following interval)")
    if np.any(duration <= 0):
        raise TimelineError(f"track {track.number}: non-positive sample duration")
    return duration


def _build_track(track: Track, mp4_id: int, bucket: TrackSamples, scale_ns: int,
                 shift_ns: int, timescale: int, mode: str) -> TrackTimeline:
    pts_ns = bucket.pts_ns
    if pts_ns.size == 0:
        raise TimelineError(f"track {track.number} has no samples")
    known = bucket.duration_ns
    default = np.full(pts_ns.shape, track.default_duration_ns or 0, dtype=np.int64)
    durations_ns = _resolve_durations(pts_ns, known, default, track)
    if mode == "preserve" and pts_ns.size > 1:
        gaps = pts_ns[1:] - pts_ns[:-1]
        if bool(np.any(gaps <= 0)):
            raise TimelineError(
                f"track {track.number}: PTS must strictly increase in "
                f"decode order for preserve mode (duplicate or reversed "
                f"timestamp)")
    if shift_ns > 0 and np.any(pts_ns < INT64_MIN + shift_ns):
        raise TimebaseOverflow("PTS shift underflows int64")
    if shift_ns < 0 and np.any(pts_ns > INT64_MAX + shift_ns):
        raise TimebaseOverflow("PTS shift overflows int64")
    global_pts = ns_to_ticks(pts_ns - shift_ns, timescale)
    duration = ns_to_ticks(durations_ns, timescale)
    if mode == "preserve":
        if global_pts.size > 1:
            # Preserve represents timing only through stts (ctts stays zero),
            # so durations must be the *rounded* PTS deltas.  Deriving them
            # from rounded nanosecond gaps could drift by a tick per sample
            # and desynchronise the edit list from the sample table.
            if bool(np.any(global_pts[1:] < global_pts[:-1])):
                position = int(np.flatnonzero(global_pts[1:] < global_pts[:-1])[0])
                raise TimelineError(
                    f"track {track.number}: PTS is not monotonic in decode order "
                    f"(sample {position + 2} at {int(global_pts[position + 1])} "
                    f"precedes {int(global_pts[position])}); reordered frames "
                    f"require --mode rewrite")
            last = ns_to_ticks(int(durations_ns[-1]), timescale)
            if last <= 0:
                raise TimelineError(
                    f"track {track.number}: final sample duration rounds to "
                    f"zero at timescale {timescale}")
            duration = np.concatenate((np.diff(global_pts),
                                       np.asarray([last], np.int64)))
    if np.any(duration <= 0):
        raise TimelineError(
            f"track {track.number}: sample duration rounds to zero at timescale "
            f"{timescale}")
    if np.any(duration > UINT32_MAX):
        raise TimelineError(
            f"track {track.number}: sample duration exceeds uint32 stts range")
    media_base = int(global_pts.min())
    media_pts = global_pts - np.int64(media_base)
    # Both modes store zero-based DTS inside the track; ``edts``/``elst``
    # delays the track by ``media_base`` on the movie timeline.
    dts = np.zeros_like(duration)
    if duration.size > 1:
        if int(duration[:-1].sum()) > INT64_MAX:
            raise TimebaseOverflow("cumulative DTS overflows int64")
        dts[1:] = np.cumsum(duration[:-1], dtype=np.int64)
    if mode == "rewrite":
        ctts = media_pts.astype(np.int64) - dts
        too_big = np.any(ctts > INT32_MAX) or np.any(ctts < INT32_MIN)
        if too_big:
            raise TimelineError(
                f"track {track.number}: composition offset exceeds signed int32")
    else:
        # Preserve durations are the rounded PTS deltas, so the sample table
        # reproduces PTS exactly and no composition offset is needed.
        if not np.array_equal(dts, media_pts):
            raise TimelineError(
                f"track {track.number}: preserve timeline produced a "
                f"composition offset")
        ctts = np.zeros_like(duration)
    ends = media_pts + duration
    duration_ticks = max(int(duration.sum()), int(ends.max()))
    if duration_ticks < 0 or media_base + duration_ticks > INT64_MAX:
        raise TimebaseOverflow("track media duration overflows int64")
    return TrackTimeline(
        track=track, mp4_track_id=mp4_id, pts=global_pts, media_pts=media_pts,
        dts=dts, ctts=ctts, duration=duration,
        offset=bucket.offset, size=bucket.size, keyframe=bucket.keyframe,
        media_base=media_base,
        first_pts=int(global_pts.min()), last_pts=int(global_pts.max()),
        max_pts=int(global_pts.max()), duration_ticks=duration_ticks)


def prepare(mkv: ParsedMatroska, timescale: int, mode: str) -> Timeline:
    """Build and validate the shared timeline used by both export modes."""

    if mode not in ("preserve", "rewrite"):
        raise ValueError(f"unknown mode {mode!r}")
    if timescale <= 0 or timescale > 1_000_000_000:
        raise TimelineError("timescale must be between 1 and 1000000000")
    data = _collect(mkv)
    if mkv.frame_count == 0:
        raise TimelineError("input has no media samples")
    global_min_ns = min(int(bucket.pts_ns.min()) for bucket in data.values()
                        if bucket.pts_ns.size)
    global_max_ns = max(int(bucket.pts_ns.max()) for bucket in data.values()
                        if bucket.pts_ns.size)
    if mode == "preserve":
        if global_min_ns < 0:
            raise TimelineError(
                f"preserve mode cannot represent a negative global start "
                f"({global_min_ns} ns); use --mode rewrite to normalize")
        shift_ns = 0
    else:
        shift_ns = global_min_ns
    tracks: list[TrackTimeline] = []
    for mp4_id, track in enumerate(mkv.tracks, start=1):
        item = _build_track(track, mp4_id, data[track.number], mkv.timestamp_scale_ns,
                            shift_ns, timescale, mode)
        tracks.append(item)
    first = ns_to_ticks(global_min_ns - shift_ns, timescale)
    last = ns_to_ticks(global_max_ns - shift_ns, timescale)
    if mode == "rewrite" and first != 0:
        raise TimelineError("global earliest PTS did not normalize to zero")
    ctts_tracks = sum(1 for item in tracks if item.has_ctts)
    source_first = int(ns_to_ticks(global_min_ns, timescale))
    return Timeline(timescale=timescale, mode=mode, tracks=tracks, shift_ns=shift_ns,
                    global_first_pts=int(first), global_last_pts=int(last),
                    sample_count=int(mkv.frame_count), ctts_tracks=ctts_tracks,
                    source_first_pts=source_first, normalized=shift_ns != 0)
