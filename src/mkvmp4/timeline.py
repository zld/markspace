"""Timeline construction: Matroska nanosecond timestamps -> MP4 time bases.

Responsibilities:
  * shift every track by one global offset so the earliest sample is at 0;
  * preserve negative block-relative time semantics (they simply shift too);
  * convert to a per-track MP4 timescale with round-half-even semantics;
  * guarantee non-regressing per-track timestamps, keeping duplicates in order;
  * detect cluster timecode regression, overflow, missing data and odd gaps.

NumPy performs the bulk boundary checks and interval statistics.
"""

from __future__ import annotations

from dataclasses import dataclass, field

import numpy as np

from .matroska import MatroskaError, MatroskaFile, Track

VIDEO_TIMESCALE = 90_000
DEFAULT_AUDIO_TIMESCALE = 48_000
INT64_MAX = np.iinfo(np.int64).max
# A gap exceeding this multiple of the median interval counts as anomalous.
GAP_ANOMALY_FACTOR = 10.0


class TimelineError(MatroskaError):
    """Raised when timestamps cannot be made valid for MP4 output."""


@dataclass(slots=True)
class TrackTimeline:
    track_number: int
    timescale: int
    sample_times: list[int]        # non-decreasing, first sample at >= 0
    sample_durations: list[int]    # one entry per sample; last may be estimated
    first_seconds: float
    last_seconds: float
    last_duration_estimated: bool
    warnings: list[str] = field(default_factory=list)

    @property
    def sample_count(self) -> int:
        return len(self.sample_times)


def _timescale_for(track: Track) -> int:
    if track.type == 1:  # video
        return VIDEO_TIMESCALE
    if track.sampling_frequency and track.sampling_frequency > 0:
        return int(round(track.sampling_frequency))
    return DEFAULT_AUDIO_TIMESCALE


def build_timelines(
    mkv: MatroskaFile, accepted_numbers: list[int]
) -> tuple[dict[int, TrackTimeline], list[str], int]:
    """Build per-track timelines.

    Returns ``(timelines, warnings, global_shift_ns)`` where the shift is the
    value subtracted from every Matroska timestamp.
    """
    warnings: list[str] = []
    _check_cluster_timecodes(mkv)

    all_times = np.asarray(
        [s.abs_time_ns for s in mkv.samples], dtype=np.int64
    )
    if all_times.size == 0:
        raise TimelineError("no samples available to build a timeline")
    if np.any(all_times > INT64_MAX) or np.any(all_times < -INT64_MAX):
        raise TimelineError("timestamp outside signed 64-bit range (overflow)")

    shift_ns = int(all_times.min().item())  # may be negative; subtract it
    shifted = all_times - np.int64(shift_ns)
    if np.any(shifted < 0) or np.any(shifted > INT64_MAX):
        raise TimelineError("shifted timestamps overflow the 64-bit range")

    timelines: dict[int, TrackTimeline] = {}
    track_by_num = {t.number: t for t in mkv.tracks}
    for number in accepted_numbers:
        track = track_by_num[number]
        idx = [i for i, s in enumerate(mkv.samples) if s.track_number == number]
        if not idx:
            warnings.append(
                f"track {number} ({track.codec_id}) has no samples; skipped"
            )
            continue
        tl = _build_one(track, shifted[np.asarray(idx, dtype=np.int64)],
                        mkv, warnings)
        timelines[number] = tl
    if not timelines:
        raise TimelineError("no accepted track contains any samples")
    return timelines, warnings, shift_ns


def _check_cluster_timecodes(mkv: MatroskaFile) -> None:
    tcs = np.asarray(mkv.cluster_timecodes_ns, dtype=np.int64)
    if tcs.size >= 2 and np.any(np.diff(tcs) < 0):
        bad = int(np.argmax(np.diff(tcs) < 0))
        raise TimelineError(
            f"cluster timecode regression between clusters {bad} and {bad + 1}: "
            f"{tcs[bad]} -> {tcs[bad + 1]} ns"
        )


def _build_one(
    track: Track,
    times_ns: np.ndarray,
    mkv: MatroskaFile,
    warnings: list[str],
) -> TrackTimeline:
    timescale = _timescale_for(track)
    twarns: list[str] = []
    scale = np.float64(timescale) / np.float64(1_000_000_000)
    rounded = np.rint(times_ns.astype(np.float64) * scale).astype(np.int64)

    # Non-regression: clamp while retaining duplicates in sample order.
    diffs = np.diff(rounded)
    if np.any(diffs < 0):
        count = int(np.count_nonzero(diffs < 0))
        twarns.append(
            f"track {track.number}: {count} timestamp regression(s) after "
            "rounding; clamped to previous timestamp"
        )
        rounded = np.maximum.accumulate(rounded)

    # Anomalous intervals (only meaningful with at least three samples).
    if rounded.size >= 3:
        gaps = np.diff(rounded).astype(np.float64)
        median_gap = float(np.median(gaps))
        if median_gap > 0:
            anomalies = np.flatnonzero(gaps > median_gap * GAP_ANOMALY_FACTOR)
            for pos in anomalies:
                twarns.append(
                    f"track {track.number}: anomalous gap of {int(gaps[pos])} "
                    f"timescale units before sample {int(pos) + 1} "
                    f"(median interval {median_gap:.1f})"
                )

    sample_times = [int(x) for x in rounded]
    sample_durations, estimated_last = _derive_durations(
        track, rounded, timescale, twarns
    )
    first_s = sample_times[0] / timescale
    last_s = sample_times[-1] / timescale
    warnings.extend(twarns)
    return TrackTimeline(
        track_number=track.number,
        timescale=timescale,
        sample_times=sample_times,
        sample_durations=sample_durations,
        first_seconds=first_s,
        last_seconds=last_s,
        last_duration_estimated=estimated_last,
        warnings=twarns,
    )


def _derive_durations(
    track: Track,
    rounded: np.ndarray,
    timescale: int,
    twarns: list[str],
) -> tuple[list[int], bool]:
    n = rounded.size
    durations = [0] * n
    deltas = np.diff(rounded)
    for i in range(n - 1):
        durations[i] = max(0, int(deltas[i]))

    estimated = False
    if n == 1:
        median_delta = None
    else:
        median_delta = int(np.median(deltas.astype(np.float64)))
    if track.default_duration_ns and track.default_duration_ns > 0:
        last_dur = int(round(
            track.default_duration_ns * timescale / 1_000_000_000))
        estimated = True
        twarns.append(
            f"track {track.number}: last sample duration derived from "
            "DefaultDuration (no explicit end timestamp)"
        )
    elif median_delta is not None and median_delta > 0:
        last_dur = median_delta
        estimated = True
        twarns.append(
            f"track {track.number}: last sample duration estimated from the "
            "median interval; exact end time unavailable"
        )
    else:
        last_dur = 0
        twarns.append(
            f"track {track.number}: cannot derive last sample duration; "
            "recorded as 0 instead of fabricating an end time"
        )
    durations[-1] = last_dur
    return durations, estimated
