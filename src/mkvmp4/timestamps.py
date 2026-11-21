"""Timestamp rewriting: Matroska ticks -> nanoseconds -> MP4 timescale.

Integer-only arithmetic throughout.  NumPy performs the vectorised conversion
and the batch monotonicity validation; every overflow check is explicit so a
wrapped 64-bit value can never masquerade as a plausible timestamp.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Iterable, Literal

import numpy as np

from .errors import TimestampPolicyError

INT64_MAX = (1 << 63) - 1
INT64_MIN = -(1 << 63)
NS_PER_MS = 1_000_000
NS_PER_SECOND = 1_000_000_000

Rounding = Literal["round", "floor", "ceil"]
Mode = Literal["strict", "repair"]


@dataclass(frozen=True)
class SampleInput:
    """One frame as read from the container, before time rewriting."""

    track: int
    cluster_timestamp: int
    block_timecode: int
    intra_block_index: int
    intra_block_count: int
    data_offset: int
    data_length: int
    keyframe: bool
    default_duration_ns: int | None = None
    block_duration: int | None = None  # in TimestampScale ticks


@dataclass
class TrackTimeline:
    track: int
    ticks: np.ndarray  # first presentation time per sample, target timescale
    durations: np.ndarray  # per-sample duration in target timescale ticks
    keyframes: np.ndarray
    data_offsets: np.ndarray  # into the input file
    data_lengths: np.ndarray
    stats: dict[str, int] = field(default_factory=dict)

    @property
    def sample_count(self) -> int:
        return int(self.ticks.shape[0])

    def to_dict(self) -> dict[str, object]:
        return {
            "track": self.track,
            "sample_count": self.sample_count,
            "first_tick": int(self.ticks[0]) if self.sample_count else None,
            "last_tick": int(self.ticks[-1]) if self.sample_count else None,
            "last_end_tick": (
                int(self.ticks[-1] + self.durations[-1]) if self.sample_count else None
            ),
            "keyframes": int(self.keyframes.sum()),
            **self.stats,
        }


@dataclass
class TimelineReport:
    tracks: dict[int, TrackTimeline]
    timescale: int
    offset_ns: int
    mode: str
    rounding: str

    def to_dict(self) -> dict[str, object]:
        return {
            "timescale": self.timescale,
            "offset_ns": self.offset_ns,
            "mode": self.mode,
            "rounding": self.rounding,
            "tracks": [t.to_dict() for t in self.tracks.values()],
            "totals": {
                key: sum(t.stats.get(key, 0) for t in self.tracks.values())
                for key in (
                    "clipped",
                    "rounded",
                    "non_monotonic",
                    "zero_duration",
                    "gaps",
                    "last_duration_fallback",
                )
            },
        }


def _checked_tick_base(samples: Iterable[SampleInput], timestamp_scale: int) -> np.ndarray:
    """Rows of ``cluster + relative`` timestamps with int64 overflow guards."""
    rows = [(s.cluster_timestamp, s.block_timecode) for s in samples]
    if not rows:
        return np.zeros(0, dtype=np.int64)
    cluster = np.asarray([r[0] for r in rows], dtype=np.int64)
    relative = np.asarray([r[1] for r in rows], dtype=np.int64)
    # Conservative bounds for the pairwise sums, computed in Python ints so a
    # wrapped NumPy addition cannot slip through.
    lower = int(cluster.min()) + int(relative.min())
    upper = int(cluster.max()) + int(relative.max())
    if lower < INT64_MIN or upper > INT64_MAX:
        raise TimestampPolicyError(
            "cluster/block timestamp addition overflows int64",
            stage="timestamps",
        )
    combined = cluster + relative
    limit = INT64_MAX // timestamp_scale
    worst = max(abs(int(combined.max())), abs(int(combined.min())))
    if worst > limit:
        raise TimestampPolicyError(
            f"timestamp {worst} TimestampScale ticks cannot be converted to "
            f"nanoseconds without 64-bit overflow",
            stage="timestamps",
        )
    return combined


def _intra_block_offsets_ns(
    samples: list[SampleInput], timestamp_scale: int, *, mode: str
) -> np.ndarray:
    """Offset added inside a laced block so each frame gets its own instant."""
    extras = np.zeros(len(samples), dtype=np.int64)
    for i, sample in enumerate(samples):
        if sample.intra_block_index == 0:
            continue
        if sample.default_duration_ns:
            step = sample.default_duration_ns
        elif sample.block_duration is not None and sample.intra_block_count > 0:
            total = sample.block_duration * timestamp_scale
            step = total // sample.intra_block_count
        else:
            if mode == "strict":
                raise TimestampPolicyError(
                    f"track {sample.track}: laced block has {sample.intra_block_count} "
                    "frames but no DefaultDuration or BlockDuration to space them",
                    offset=sample.data_offset,
                    track=sample.track,
                    sample_index=sample.intra_block_index,
                    stage="timestamps",
                )
            step = 0
        extras[i] = step * sample.intra_block_index
    return extras


def rewrite_timeline(
    samples: Iterable[SampleInput],
    *,
    timestamp_scale: int,
    timescale: int,
    offset_ms: int = 0,
    mode: Mode = "strict",
    rounding: Rounding = "round",
    clip_negative: bool = True,
    max_gap_ms: int | None = None,
) -> TimelineReport:
    """Convert all samples and enforce / repair per-track monotonicity."""
    if timestamp_scale <= 0:
        raise TimestampPolicyError("TimestampScale must be positive", stage="timestamps")
    if timescale <= 0:
        raise TimestampPolicyError("MP4 timescale must be positive", stage="timestamps")
    if mode not in ("strict", "repair"):
        raise TimestampPolicyError(f"unknown policy mode {mode!r}", stage="timestamps")
    if rounding not in ("round", "floor", "ceil"):
        raise TimestampPolicyError(f"unknown rounding mode {rounding!r}", stage="timestamps")

    sample_list = list(samples)
    base_ticks = _checked_tick_base(sample_list, timestamp_scale)
    extras = _intra_block_offsets_ns(sample_list, timestamp_scale, mode=mode)

    ns = base_ticks * np.int64(timestamp_scale)
    if extras.size:
        if int(extras.max()) > INT64_MAX - int(ns.max()):
            raise TimestampPolicyError(
                "intra-block lacing offset overflows int64 nanoseconds",
                stage="timestamps",
            )
        ns = ns + extras

    offset_ns = offset_ms * NS_PER_MS
    if offset_ns >= 0:
        if int(ns.max()) > INT64_MAX - offset_ns:
            raise TimestampPolicyError(
                "uniform offset would overflow int64 nanoseconds",
                stage="timestamps",
            )
    else:
        if int(ns.min()) < INT64_MIN - offset_ns:
            raise TimestampPolicyError(
                "uniform offset would underflow int64 nanoseconds",
                stage="timestamps",
            )
    shifted = ns + np.int64(offset_ns)

    clipped_mask = shifted < 0
    clipped_count = int(clipped_mask.sum())
    if clipped_count and not clip_negative:
        first = int(np.flatnonzero(clipped_mask)[0])
        sample = sample_list[first]
        raise TimestampPolicyError(
            f"track {sample.track}: sample {first} is negative after the "
            f"{offset_ms} ms offset and negative clipping is disabled",
            offset=sample.data_offset,
            track=sample.track,
            sample_index=first,
            stage="timestamps",
        )
    shifted = np.where(clipped_mask, np.int64(0), shifted)

    ticks, rounded = _to_timescale(shifted, timescale, timestamp_scale, rounding)

    # Group sample indices by track, preserving container (decode) order.
    by_track: dict[int, list[int]] = {}
    for index, sample in enumerate(sample_list):
        by_track.setdefault(sample.track, []).append(index)

    timelines: dict[int, TrackTimeline] = {}
    for track, indices in by_track.items():
        order = np.asarray(indices, dtype=np.int64)
        track_ticks = ticks[order]
        track_rounded = rounded[order]
        track_clipped = clipped_mask[order]
        stats = {
            "clipped": int(track_clipped.sum()),
            "rounded": int(track_rounded.sum()),
            "non_monotonic": 0,
            "zero_duration": 0,
            "gaps": 0,
            "last_duration_fallback": 0,
        }
        track_ticks, stats_update = _enforce_monotonic(
            track_ticks,
            [sample_list[i] for i in indices],
            mode=mode,
            max_gap_ticks=(
                None if max_gap_ms is None else max_gap_ms * timescale // 1000
            ),
            track=track,
        )
        stats.update(stats_update)
        durations, fallback = _durations(
            track_ticks,
            [sample_list[i] for i in indices],
            timestamp_scale=timestamp_scale,
            timescale=timescale,
            mode=mode,
            track=track,
        )
        stats["last_duration_fallback"] += fallback
        timelines[track] = TrackTimeline(
            track=track,
            ticks=track_ticks,
            durations=durations,
            keyframes=np.asarray(
                [sample_list[i].keyframe for i in indices], dtype=bool
            ),
            data_offsets=np.asarray(
                [sample_list[i].data_offset for i in indices], dtype=np.int64
            ),
            data_lengths=np.asarray(
                [sample_list[i].data_length for i in indices], dtype=np.int64
            ),
            stats=stats,
        )

    return TimelineReport(
        tracks=timelines,
        timescale=timescale,
        offset_ns=offset_ns,
        mode=mode,
        rounding=rounding,
    )


def _to_timescale(
    ns: np.ndarray, timescale: int, timestamp_scale: int, rounding: Rounding
) -> tuple[np.ndarray, np.ndarray]:
    """Exact ns -> ticks with explicit overflow detection and rounding count."""
    if ns.size == 0:
        return np.zeros(0, dtype=np.int64), np.zeros(0, dtype=bool)
    worst = int(ns.max())
    if worst > INT64_MAX // timescale:
        raise TimestampPolicyError(
            f"timestamp {worst} ns cannot be scaled to a {timescale} Hz timescale "
            "without 64-bit overflow",
            stage="timestamps",
        )
    scaled = ns * np.int64(timescale)
    quotient, remainder = np.divmod(scaled, np.int64(timestamp_scale))
    rounded = remainder != 0
    if rounding == "round":
        quotient = quotient + (remainder * 2 >= timestamp_scale).astype(np.int64)
    elif rounding == "ceil":
        quotient = quotient + (remainder != 0).astype(np.int64)
    return quotient, rounded


def _enforce_monotonic(
    ticks: np.ndarray,
    samples: list[SampleInput],
    *,
    mode: str,
    max_gap_ticks: int | None,
    track: int,
) -> tuple[np.ndarray, dict[str, int]]:
    """Strict: reject regressions/overlaps/gaps.  Repair: force +1 per sample."""
    stats = {"non_monotonic": 0, "gaps": 0}
    if ticks.size < 2:
        return ticks, stats
    deltas = np.diff(ticks)
    bad = np.flatnonzero(deltas <= 0)
    gap_ticks = np.zeros(0, dtype=np.int64)
    if max_gap_ticks is not None:
        gap_ticks = np.flatnonzero(deltas > max_gap_ticks)
    if mode == "strict":
        if bad.size:
            index = int(bad[0]) + 1
            sample = samples[index]
            raise TimestampPolicyError(
                f"track {track}: sample {index} is not after sample {index - 1} "
                f"({int(ticks[index])} <= {int(ticks[index - 1])})",
                offset=sample.data_offset,
                track=track,
                sample_index=index,
                stage="timestamps",
            )
        if gap_ticks.size:
            index = int(gap_ticks[0]) + 1
            sample = samples[index]
            raise TimestampPolicyError(
                f"track {track}: gap of {int(deltas[int(gap_ticks[0])])} ticks before "
                f"sample {index} exceeds the configured limit of {max_gap_ticks}",
                offset=sample.data_offset,
                track=track,
                sample_index=index,
                stage="timestamps",
            )
        return ticks, stats

    # Repair: each sample must be at least one tick after its predecessor.
    # max(ticks[i], ticks[i-1] + 1) accumulated == max.accumulate(ticks - i) + i
    positions = np.arange(ticks.size, dtype=np.int64)
    repaired = np.maximum.accumulate(ticks - positions) + positions
    stats["non_monotonic"] = int(bad.size)
    stats["gaps"] = int(gap_ticks.size)
    return repaired, stats


def _durations(
    ticks: np.ndarray,
    samples: list[SampleInput],
    *,
    timestamp_scale: int,
    timescale: int,
    mode: str,
    track: int,
) -> tuple[np.ndarray, int]:
    """Per-sample duration in target ticks; never zero, never fabricated silently."""
    count = ticks.size
    if count == 0:
        return np.zeros(0, dtype=np.int64), 0
    durations = np.zeros(count, dtype=np.int64)
    if count > 1:
        durations[:-1] = np.diff(ticks)
    zero = np.flatnonzero(durations[:-1] <= 0)
    if zero.size:
        index = int(zero[0]) + 1
        sample = samples[index]
        raise TimestampPolicyError(
            f"track {track}: sample {index} has zero duration after rewriting",
            offset=sample.data_offset,
            track=track,
            sample_index=index,
            stage="timestamps",
        )
    last = samples[-1]
    last_duration = 0
    if last.default_duration_ns:
        last_duration = _ns_to_ticks(last.default_duration_ns, timestamp_scale, timescale)
    if last_duration <= 0 and count > 1:
        last_duration = int(durations[-2])
    if last_duration <= 0:
        if mode == "strict":
            raise TimestampPolicyError(
                f"track {track}: cannot determine a positive duration for the last sample",
                offset=last.data_offset,
                track=track,
                sample_index=count - 1,
                stage="timestamps",
            )
        last_duration = 1
        durations[-1] = 1
        return durations, 1
    durations[-1] = last_duration
    return durations, 0


def _ns_to_ticks(value_ns: int, timestamp_scale: int, timescale: int) -> int:
    """Rounded, overflow-checked conversion used for default durations."""
    if value_ns > INT64_MAX // timescale:
        raise TimestampPolicyError(
            f"duration {value_ns} ns overflows when scaled to {timescale} Hz",
            stage="timestamps",
        )
    num = value_ns * timescale
    quotient, remainder = divmod(num, timestamp_scale)
    if remainder * 2 >= timestamp_scale:
        quotient += 1
    return quotient
