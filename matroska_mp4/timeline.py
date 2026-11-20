"""Shared timeline: PTS extraction, duration resolution, compact sample index.

Samples are held as NumPy int64 arrays (never one Python object per frame) so a
five-million-sample input keeps only a compact index in memory.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np

from .matroska import ParsedMatroska, Track

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


def _check_i64(value: int, what: str) -> int:
    if value < INT64_MIN or value > INT64_MAX:
        raise TimebaseOverflow(f"{what} {value} overflows int64")
    return value


def ticks_to_ns(ticks: np.ndarray | int, scale_ns: int) -> np.ndarray | int:
    """Multiply tick counts by a positive scale, checking int64 bounds."""

    if scale_ns <= 0:
        raise TimelineError("TimestampScale must be positive")
    array = np.asarray(ticks, dtype=np.int64)
    if np.any(array > INT64_MAX // scale_ns) or np.any(array < INT64_MIN // scale_ns):
        raise TimebaseOverflow("timestamp * TimestampScale overflows int64")
    result = array * np.int64(scale_ns)
    return int(result) if np.isscalar(ticks) else result


def ns_to_ticks(ns: np.ndarray | int, timescale: int) -> np.ndarray | int:
    """Round nanoseconds to target ticks (half away from zero), int64-safe."""

    if timescale <= 0:
        raise TimelineError("MP4 timescale must be positive")
    array = np.asarray(ns, dtype=np.int64)
    if np.any(array > INT64_MAX // timescale) or np.any(array < INT64_MIN // timescale):
        raise TimebaseOverflow("timestamp * timescale overflows int64")
    product = array * np.int64(timescale)
    magnitude = np.abs(product)
    quotient = magnitude // np.int64(NS_PER_S)
    remainder = magnitude % np.int64(NS_PER_S)
    rounded = quotient + (remainder >= np.int64(NS_PER_S // 2))
    result = np.where(product < 0, -rounded, rounded).astype(np.int64)
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

    def track(self, mkv_number: int) -> TrackTimeline:
        for item in self.tracks:
            if item.track.number == mkv_number:
                return item
        raise KeyError(mkv_number)


def _collect(mkv: ParsedMatroska) -> dict[int, dict[str, list[int] | list[bool]]]:
    tracks = {t.number: t for t in mkv.tracks}
    data: dict[int, dict[str, list[int] | list[bool]]] = {
        t.number: {"pts": [], "dur": [], "default": [], "offset": [], "size": [],
                   "key": []} for t in mkv.tracks}
    for block in mkv.blocks:
        track = tracks[block.track_number]
        count = len(block.frames)
        block_pts = _check_i64(block.timecode * mkv.timestamp_scale_ns,
                               "block PTS in nanoseconds")
        block_dur = None
        if block.duration_ticks is not None:
            block_dur = _check_i64(block.duration_ticks * mkv.timestamp_scale_ns,
                                   "BlockDuration in nanoseconds")
        per_frame = None
        if count > 1:
            if block_dur is not None and block_dur % count == 0:
                per_frame = block_dur // count
            elif track.default_duration_ns:
                per_frame = track.default_duration_ns
            else:
                raise TimelineError(
                    f"track {track.number}: laced block at offset {block.offset} "
                    f"has {count} frames but no derivable per-frame duration")
        bucket = data[track.number]
        for index, frame in enumerate(block.frames):
            pts = block_pts + (per_frame * index if per_frame is not None else 0)
            _check_i64(pts, "sample PTS")
            duration = per_frame if count > 1 else block_dur
            bucket["pts"].append(pts)
            bucket["dur"].append(duration or 0)
            bucket["default"].append(track.default_duration_ns or 0)
            bucket["offset"].append(frame.offset)
            bucket["size"].append(frame.size)
            bucket["key"].append(block.keyframe)
    return data


def _resolve_durations(pts: np.ndarray, known: np.ndarray,
                       default: np.ndarray, track: Track) -> np.ndarray:
    duration = known.copy()
    presentation = np.argsort(pts, kind="stable")
    for position, index in enumerate(presentation.tolist()):
        if duration[index] > 0:
            continue
        if default[index] > 0:
            duration[index] = default[index]
            continue
        if position + 1 < presentation.size:
            nxt = int(presentation[position + 1])
            interval = int(pts[nxt]) - int(pts[index])
            if interval > 0:
                duration[index] = interval
                continue
        position_kind = "final" if position == presentation.size - 1 else "sample"
        raise TimelineError(
            f"track {track.number}: cannot determine duration of {position_kind} "
            f"at PTS {int(pts[index])} ns (no BlockDuration, DefaultDuration, "
            f"or following interval)")
    if np.any(duration <= 0):
        raise TimelineError(f"track {track.number}: non-positive sample duration")
    return duration


def _build_track(track: Track, mp4_id: int, bucket: dict, scale_ns: int,
                 shift_ns: int, timescale: int, mode: str) -> TrackTimeline:
    pts_ns = np.asarray(bucket["pts"], dtype=np.int64)
    if pts_ns.size == 0:
        raise TimelineError(f"track {track.number} has no samples")
    known = np.asarray(bucket["dur"], dtype=np.int64)
    default = np.asarray(bucket["default"], dtype=np.int64)
    durations_ns = _resolve_durations(pts_ns, known, default, track)
    scale = np.int64(scale_ns)
    if np.any(pts_ns > INT64_MAX // scale_ns) or np.any(pts_ns < INT64_MIN // scale_ns):
        raise TimebaseOverflow("PTS * TimestampScale overflows int64")
    if np.any(durations_ns > INT64_MAX // scale_ns):
        raise TimebaseOverflow("duration * TimestampScale overflows int64")
    global_pts = ns_to_ticks(pts_ns - shift_ns, timescale)
    duration = ns_to_ticks(durations_ns, timescale)
    if np.any(duration <= 0):
        raise TimelineError(
            f"track {track.number}: sample duration rounds to zero at timescale "
            f"{timescale}")
    if np.any(duration > UINT32_MAX):
        raise TimelineError(
            f"track {track.number}: sample duration exceeds uint32 stts range")
    decode_pts = [int(v) for v in global_pts.tolist()]
    if mode == "preserve":
        if any(b < a for a, b in zip(decode_pts, decode_pts[1:])):
            raise TimelineError(
                f"track {track.number}: PTS is not monotonic in decode order; "
                f"reordered frames require --mode rewrite")
        dts = global_pts.copy()
    media_base = int(global_pts.min())
    media_pts = global_pts - np.int64(media_base)
    if mode == "rewrite":
        dts = np.zeros_like(duration)
        if duration.size > 1:
            dts[1:] = np.cumsum(duration[:-1], dtype=np.int64)
        ctts = media_pts - dts
        too_big = np.any(ctts > INT32_MAX) or np.any(ctts < INT32_MIN)
        if too_big:
            raise TimelineError(
                f"track {track.number}: composition offset exceeds signed int32")
    else:
        dts = media_pts.copy()
        ctts = np.zeros_like(media_pts)
    ends = media_pts + duration
    duration_ticks = max(int(duration.sum()), int(ends.max()))
    return TrackTimeline(
        track=track, mp4_track_id=mp4_id, pts=global_pts, media_pts=media_pts,
        dts=dts, ctts=ctts, duration=duration,
        offset=np.asarray(bucket["offset"], dtype=np.int64),
        size=np.asarray(bucket["size"], dtype=np.int64),
        keyframe=np.asarray(bucket["key"], dtype=bool), media_base=media_base,
        first_pts=decode_pts[0], last_pts=decode_pts[-1],
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
    all_pts = [pts for bucket in data.values() for pts in bucket["pts"]]
    global_min_ns = min(all_pts)
    global_max_ns = max(all_pts)
    shift_ns = global_min_ns if (mode == "rewrite" or global_min_ns < 0) else 0
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
    return Timeline(timescale=timescale, mode=mode, tracks=tracks, shift_ns=shift_ns,
                    global_first_pts=int(first), global_last_pts=int(last),
                    sample_count=int(mkv.frame_count), ctts_tracks=ctts_tracks)
