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
    input_offsets: list[int]
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

