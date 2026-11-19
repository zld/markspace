"""Timestamp diagnostics and deterministic repair.

Input frames are already in presentation order (see :mod:`frameaudit.reorder`).
For each frame the original PTS is inspected and four fault classes are
recognized:

* ``missing``    -- the container provides no PTS;
* ``duplicate``  -- equal to the previous frame's PTS;
* ``regression`` -- smaller than the previous frame's PTS;
* ``jump``       -- the delta to the previous PTS exceeds the configured
  threshold in seconds.

Repair assigns a strictly increasing timestamp derived from the previous
accepted timestamp plus the nominal frame interval.  Frames that need no
repair keep their exact original PTS, so variable-frame-rate material is not
quantized onto a fixed clock.  All comparisons use seconds computed from each
frame's own time base; the original integer PTS and time base are preserved on
the record.
"""

from __future__ import annotations

from fractions import Fraction
from typing import Optional

from .errors import UsageError
from .model import FrameRecord, NormalizedFrame

REASON_MISSING = "missing"
REASON_DUPLICATE = "duplicate"
REASON_REGRESSION = "regression"
REASON_JUMP = "jump"


def parse_fraction(value: str) -> Fraction:
    """Parse ``"30000/1001"`` or ``"29.97"`` style values into a Fraction."""
    text = value.strip()
    if not text:
        raise UsageError("frame rate must be a non-empty value, e.g. 30000/1001")
    try:
        if "/" in text:
            numerator, denominator = text.split("/", 1)
            result = Fraction(int(numerator), int(denominator))
        else:
            result = Fraction(text)
    except (ValueError, ZeroDivisionError) as exc:
        raise UsageError(f"invalid frame rate or interval value: {value!r}") from exc
    if result <= 0:
        raise UsageError(f"frame rate or interval must be positive, got {value!r}")
    return result


def frame_interval_seconds(
    fps: Optional[Fraction],
    explicit_interval: Optional[Fraction],
) -> float:
    """Resolve the repair interval in seconds from rate or explicit value."""
    if explicit_interval is not None:
        if explicit_interval <= 0:
            raise UsageError("frame interval must be positive")
        return float(explicit_interval)
    if fps is None:
        raise UsageError(
            "could not determine frame rate; pass --fps or --frame-interval"
        )
    if fps <= 0:
        raise UsageError("frame rate must be positive")
    return 1.0 / float(fps)


def repair_timestamps(
    frames: list[FrameRecord],
    fps: Optional[Fraction] = None,
    jump_threshold: float = 2.0,
    explicit_interval: Optional[Fraction] = None,
) -> tuple[list[NormalizedFrame], int, float]:
    """Repair PTS values on presentation-ordered frames.

    Returns the normalized frames (same length and order as the input), the
    number of repaired timestamps and the maximum absolute jump in seconds
    observed between consecutive frames that carried an original PTS.
    """
    if jump_threshold <= 0:
        raise UsageError(
            f"jump threshold must be positive, got {jump_threshold}"
        )
    interval = frame_interval_seconds(fps, explicit_interval)

    first_valid_seconds: Optional[float] = next(
        (frame.pts_seconds() for frame in frames if frame.pts_seconds() is not None),
        None,
    )
    anchor_seconds = first_valid_seconds if first_valid_seconds is not None else 0.0
    # Index of the first frame carrying a usable PTS, if any.
    anchor_index = next(
        (i for i, frame in enumerate(frames) if frame.pts_seconds() is not None),
        None,
    )

    normalized: list[NormalizedFrame] = []
    repaired_count = 0
    max_jump_seconds = 0.0
    # Output clock: last timestamp accepted into the strictly increasing series.
    last_kept_seconds: Optional[float] = None
    # Diagnosis reference: PTS of the preceding frame as stored in the input.
    previous_original_seconds: Optional[float] = None

    for order_index, frame in enumerate(frames):
        original_seconds = frame.pts_seconds()
        reasons: list[str] = []

        # ---- Phase 1: diagnose against the ORIGINAL timestamp sequence ----
        if original_seconds is None:
            reasons.append(REASON_MISSING)
        elif previous_original_seconds is not None:
            delta = original_seconds - previous_original_seconds
            if abs(delta) > max_jump_seconds:
                max_jump_seconds = abs(delta)
            if delta == 0:
                reasons.append(REASON_DUPLICATE)
            elif delta < 0:
                reasons.append(REASON_REGRESSION)
            elif delta > jump_threshold:
                reasons.append(REASON_JUMP)

        # ---- Phase 2: assign a strictly increasing output timestamp -------
        # A frame keeps its original PTS only when it is fault-free AND still
        # lands strictly above the previously accepted clock value.  Otherwise
        # it is placed one interval above the clock so the output stays
        # strictly increasing.  The ``repaired`` flag tracks whether the
        # frame's *own* original timestamp was faulty; a healthy frame that has
        # to step forward because an earlier fault shifted the clock is moved
        # but not counted as a repaired timestamp.
        keep_original = (
            not reasons
            and original_seconds is not None
            and (
                last_kept_seconds is None
                or original_seconds > last_kept_seconds
            )
        )
        own_fault = bool(reasons)

        if keep_original:
            assert original_seconds is not None
            repaired_seconds = original_seconds
        elif last_kept_seconds is None:
            if anchor_index is not None and order_index < anchor_index:
                # Back-fill leading missing frames relative to the anchor so
                # the last of them sits one interval below the anchor.
                gap = anchor_index - order_index
                repaired_seconds = max(
                    0.0, anchor_seconds - gap * interval
                )
            else:
                repaired_seconds = 0.0
        else:
            repaired_seconds = last_kept_seconds + interval

        last_kept_seconds = repaired_seconds
        if original_seconds is not None:
            previous_original_seconds = original_seconds
        if own_fault:
            repaired_count += 1

        normalized.append(
            NormalizedFrame(
                order_index=order_index,
                decode_index=frame.decode_index,
                frame_type=frame.frame_type,
                key_frame=frame.key_frame,
                original_pts=frame.pts,
                time_base=frame.time_base,
                original_pts_seconds=original_seconds,
                repaired_pts_seconds=repaired_seconds,
                timestamp_repaired=own_fault,
                repair_reasons=reasons,
            )
        )

    return normalized, repaired_count, max_jump_seconds
