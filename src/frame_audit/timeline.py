"""Presentation-order reconstruction and monotonic timestamp repair.

Decoded frames arrive in the codec's output order.  For a healthy stream that is
already presentation order, but an asset trimmed or re-wrapped outside its
encoder can carry missing PTS, repeated PTS, PTS that step backwards, or PTS
from a reset time base.  Three steps handle that:

1. **Classification** runs in arrival order against the highest PTS seen so
   far.  An absent value is ``missing`` (``first_frame`` when it leads the
   stream), a repeat is ``duplicate`` and a step backwards is ``regression``.
   A negative leading value is a ``first_frame`` repair.  This is what the
   report shows: it describes the file, not the repaired output.
2. **Ordering** sorts frames by an effective key.  A usable PTS is used as-is;
   a missing value keeps its emission slot by following the previous key by half
   a frame, so B frames that straddle it are not displaced.  Sorting therefore
   moves genuinely out-of-order frames into place and leaves healthy streams
   alone.
3. **Repair** walks the presentation order once and emits a strictly increasing
   timeline on the stream time base.  Missing frames are spaced with lookahead
   to the next known PTS so a dropped run leaves room for its neighbours instead
   of pushing everything behind it; duplicate and regressed values advance by
   one step.

Original and repaired timestamps are kept side by side on every record.  Nothing
here opens a container or touches pixel data, so the algorithm is directly
testable and both output sinks consume exactly the same result.
"""

from __future__ import annotations

import statistics
from dataclasses import dataclass
from fractions import Fraction
from typing import Sequence

from .model import FrameRecord, Policy, RepairKind, TimelineFrame

__all__ = [
    "TimelineData",
    "build_timeline",
    "classify_frames",
    "derive_step_ticks",
    "effective_keys",
    "presentation_order",
]


@dataclass(frozen=True, slots=True)
class TimelineData:
    """Presentation-ordered frames plus the facts derived while repairing."""

    frames: tuple[TimelineFrame, ...]
    step_ticks: int
    time_base: Fraction
    reordered_frames: int
    applied: bool

    @property
    def count(self) -> int:
        return len(self.frames)

    @property
    def repairs(self) -> int:
        """Anomalous timestamps rewritten under the repair policy."""
        return sum(1 for frame in self.frames if frame.applied)

    @property
    def anomalies(self) -> int:
        """Frames whose original timestamp could not be used as-is."""
        return sum(1 for frame in self.frames if frame.kind.repaired)

    def count_type(self, pict_type: str) -> int:
        return sum(1 for frame in self.frames if frame.pict_type == pict_type)

    @property
    def breakdown(self) -> dict[str, int]:
        """Anomalies by kind, independent of whether repair was applied."""
        counts = {kind.value: 0 for kind in RepairKind if kind.repaired}
        for frame in self.frames:
            if frame.kind.repaired:
                counts[frame.kind.value] += 1
        return counts

    @property
    def max_jump_us(self) -> int:
        values = [frame.repaired_pts_us for frame in self.frames]
        known = [value for value in values if value is not None]
        if len(known) < 2:
            return 0
        return max(abs(b - a) for a, b in zip(known, known[1:]))


def derive_step_ticks(
    timestamps: Sequence[int],
    *,
    time_base: Fraction | None,
    average_rate: Fraction | None,
) -> int:
    """Estimate one frame's duration in stream ticks.

    Deltas are taken between sorted, de-duplicated timestamps so a handful of
    anomalies cannot distort the estimate.  The declared frame rate is only a
    fallback for streams with fewer than two usable timestamps.
    """
    ordered = sorted({int(value) for value in timestamps})
    deltas = [b - a for a, b in zip(ordered, ordered[1:]) if b > a]
    if deltas:
        return max(1, int(statistics.median(deltas)))
    if time_base and average_rate and average_rate > 0:
        exact = (Fraction(1) / average_rate) / time_base
        if exact > 0:
            return max(1, int(round(exact)))
    return 1


def classify_frames(frames: Sequence[FrameRecord]) -> list[RepairKind]:
    """Classify each timestamp against the highest value seen so far."""
    kinds: list[RepairKind] = []
    highest: int | None = None
    for frame in frames:
        pts = frame.pts
        if pts is None:
            kinds.append(
                RepairKind.FIRST_FRAME if highest is None else RepairKind.MISSING
            )
        elif highest is None:
            kinds.append(RepairKind.FIRST_FRAME if pts < 0 else RepairKind.NONE)
        elif pts <= highest:
            kinds.append(
                RepairKind.DUPLICATE if pts == highest else RepairKind.REGRESSION
            )
        else:
            kinds.append(RepairKind.NONE)
        if pts is not None and (highest is None or pts > highest):
            highest = pts
    return kinds


def effective_keys(frames: Sequence[FrameRecord], step: int) -> list[Fraction]:
    """Sort key per frame, in arrival order.

    Usable timestamps keep their exact value.  A missing value follows the
    previous key by half a frame, pinning it to the decoder's emission slot
    without colliding with a following frame exactly one step later.
    """
    half = Fraction(step, 2)
    keys: list[Fraction] = []
    previous: Fraction | None = None
    for index, frame in enumerate(frames):
        if frame.pts is not None:
            key = Fraction(int(frame.pts))
        elif previous is not None:
            key = previous + half
        else:
            key = Fraction(index * step)
        keys.append(key)
        previous = key
    return keys


def presentation_order(frames: Sequence[FrameRecord], step: int) -> list[int]:
    """Return arrival indices sorted into presentation order (stable)."""
    keys = effective_keys(frames, step)
    return sorted(range(len(frames)), key=lambda index: (keys[index], index))


def _next_known_pts(
    frames: Sequence[FrameRecord], order: Sequence[int], start: int
) -> int | None:
    """First usable original PTS after ``start`` in presentation order."""
    for position in range(start + 1, len(order)):
        pts = frames[order[position]].pts
        if pts is not None:
            return int(pts)
    return None


def _missing_run_length(
    frames: Sequence[FrameRecord], order: Sequence[int], start: int
) -> int:
    run = 0
    for position in range(start, len(order)):
        if frames[order[position]].pts is not None:
            break
        run += 1
    return max(1, run)


def _repair_pass(
    frames: Sequence[FrameRecord],
    kinds: Sequence[RepairKind],
    order: Sequence[int],
    *,
    step: int,
    policy: Policy,
) -> list[TimelineFrame]:
    applied = policy is Policy.REPAIR
    cursor: int | None = None
    timeline: list[TimelineFrame] = []

    for position, index in enumerate(order):
        frame = frames[index]
        original = frame.pts
        kind = kinds[index]

        if original is None:
            run = _missing_run_length(frames, order, position)
            upcoming = _next_known_pts(frames, order, position)
            if cursor is None:
                value: int | None = 0
            elif upcoming is not None and upcoming > cursor:
                # Leave one slot for every frame in the run, never colliding
                # with the next real timestamp.
                room = upcoming - cursor
                value = cursor + max(1, room // (run + 1))
            else:
                value = cursor + step
        elif cursor is None:
            value = 0 if original < 0 else original
        elif original <= cursor:
            upcoming = _next_known_pts(frames, order, position)
            candidate = cursor + step
            if upcoming is not None and candidate >= upcoming:
                candidate = cursor + 1
            value = candidate
        else:
            value = original

        if not applied:
            value = original
        if value is not None:
            cursor = value

        timeline.append(
            TimelineFrame(
                frame=frame,
                position=position,
                original_pts=original,
                repaired_pts=value,
                kind=kind,
                applied=applied and kind.repaired,
            )
        )
    return timeline


def build_timeline(
    frames: Sequence[FrameRecord],
    *,
    time_base: Fraction,
    average_rate: Fraction | None = None,
    policy: Policy = Policy.REPAIR,
) -> TimelineData:
    """Sort frames into presentation order and repair unusable timestamps.

    This is the single record set every reporter consumes, so the terminal table
    and the JSON output can never disagree about a repair.
    """
    ordered = list(frames)
    known = [frame.pts for frame in ordered if frame.pts is not None]
    step = derive_step_ticks(
        [int(value) for value in known],
        time_base=time_base,
        average_rate=average_rate,
    )
    kinds = classify_frames(ordered)
    order = presentation_order(ordered, step)
    timeline = _repair_pass(ordered, kinds, order, step=step, policy=policy)
    moved = sum(1 for position, index in enumerate(order) if position != index)
    return TimelineData(
        frames=tuple(timeline),
        step_ticks=step,
        time_base=time_base,
        reordered_frames=moved,
        applied=policy is Policy.REPAIR,
    )
