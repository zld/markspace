"""Rational-arithmetic PTS repair.

Every timestamp is handled as ``Fraction`` seconds derived from the frame's
own ``time_base``; repaired PTS are re-emitted in the first frame's time base
so the whole manifest shares one unit.  Repair reasons:

* ``missing``   — PTS absent; synthesised from frame rate;
* ``duplicate`` — PTS equal to the previous one;
* ``backward``  — PTS smaller than the previous one;
* ``jump``      — positive gap beyond 1.5 expected frame durations;
* ``ok``        — original PTS retained unchanged.

The frame rate comes from the stream when available; otherwise the caller must
supply an explicit ``--fps`` value.
"""

from __future__ import annotations

from dataclasses import dataclass
from fractions import Fraction

from .decode import DecodedFrame
from .errors import UsageError

# A gap larger than this multiple of one frame duration counts as a jump.
JUMP_FACTOR = Fraction(3, 2)


@dataclass(slots=True)
class FrameRecord:
    """One presentation-order frame after timestamp repair."""

    frame: DecodedFrame
    order_index: int
    original_pts: int | None
    repaired_pts: int
    time_base: Fraction
    original_pts_ms: float | None
    repaired_pts_ms: float
    repair_reason: str


@dataclass(slots=True)
class RepairResult:
    records: list[FrameRecord]
    time_base: Fraction
    frame_rate: Fraction
    repaired_count: int
    reason_counts: dict[str, int]
    max_jump_ms: float | None


def resolve_frame_rate(
    stream_rate: Fraction | None, explicit_fps: float | None
) -> Fraction:
    if explicit_fps is not None:
        if explicit_fps <= 0:
            raise UsageError(f"--fps must be positive, got {explicit_fps}")
        return Fraction(explicit_fps).limit_denominator(1_000_000)
    if stream_rate is not None and stream_rate > 0:
        return stream_rate
    raise UsageError(
        "stream has no usable frame rate; pass an explicit --fps value"
    )


def _ms(value_seconds: Fraction) -> float:
    return round(float(value_seconds * 1000), 6)


def repair_timestamps(
    frames: list[DecodedFrame],
    frame_rate: Fraction,
    jump_factor: Fraction = JUMP_FACTOR,
) -> RepairResult:
    """Repair PTS values across presentation-ordered *frames*."""
    if not frames:
        return RepairResult(
            records=[],
            time_base=Fraction(1, 1),
            frame_rate=frame_rate,
            repaired_count=0,
            reason_counts={},
            max_jump_ms=None,
        )

    time_base = frames[0].time_base
    frame_duration = 1 / frame_rate  # seconds per frame
    threshold = frame_duration * jump_factor
    pts_step = frame_duration / time_base  # repaired-PTS units per frame

    records: list[FrameRecord] = []
    reason_counts: dict[str, int] = {}
    repaired_count = 0
    max_jump_ms: float | None = None

    # Anchor: first frame keeps its PTS when it has one, else starts at 0.
    first = frames[0]
    first_orig_sec = (
        first.pts * first.time_base if first.pts is not None else None
    )
    anchor_sec = first_orig_sec if first_orig_sec is not None else Fraction(0)

    prev_sec: Fraction | None = None
    prev_repaired_pts: int | None = None

    for order_index, frame in enumerate(frames):
        original_pts = frame.pts
        original_sec = (
            original_pts * frame.time_base
            if original_pts is not None
            else None
        )

        if original_sec is None:
            reason = "missing"
            if prev_sec is None:
                target_sec = anchor_sec
            else:
                target_sec = prev_sec + frame_duration
        else:
            target_sec = original_sec
            if prev_sec is not None:
                gap = original_sec - prev_sec
                if original_pts == records[-1].original_pts:
                    reason = "duplicate"
                elif gap < 0:
                    reason = "backward"
                elif gap > threshold:
                    reason = "jump"
                else:
                    reason = "ok"
            else:
                reason = "ok"

        if reason != "ok":
            repaired_count += 1
            reason_counts[reason] = reason_counts.get(reason, 0) + 1
            if reason == "jump":
                assert original_sec is not None and prev_sec is not None
                jump_ms = _ms(original_sec - prev_sec)
                max_jump_ms = jump_ms if max_jump_ms is None else max(
                    max_jump_ms, jump_ms
                )
            # Synthesise a monotonic target one frame after the previous one.
            if prev_sec is None:
                target_sec = anchor_sec
            else:
                target_sec = prev_sec + frame_duration

        repaired_pts = round(target_sec / time_base)
        # Guarantee strict monotonic increase regardless of rounding.
        if prev_repaired_pts is not None and repaired_pts <= prev_repaired_pts:
            repaired_pts = prev_repaired_pts + max(1, round(pts_step))

        records.append(
            FrameRecord(
                frame=frame,
                order_index=order_index,
                original_pts=original_pts,
                repaired_pts=repaired_pts,
                time_base=time_base,
                original_pts_ms=(
                    _ms(original_sec) if original_sec is not None else None
                ),
                repaired_pts_ms=_ms(repaired_pts * time_base),
                repair_reason=reason,
            )
        )
        prev_sec = target_sec
        prev_repaired_pts = repaired_pts

    return RepairResult(
        records=records,
        time_base=time_base,
        frame_rate=frame_rate,
        repaired_count=repaired_count,
        reason_counts=reason_counts,
        max_jump_ms=max_jump_ms,
    )
