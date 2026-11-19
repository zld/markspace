"""Unit tests for presentation-order reconstruction and timestamp repair."""

from __future__ import annotations

from itertools import pairwise

from fractions import Fraction

import pytest

from frame_audit.model import FrameRecord, Policy, RepairKind
from frame_audit.timeline import (
    build_timeline,
    derive_step_ticks,
    effective_keys,
    presentation_order,
)

TB = Fraction(1, 90_000)


def frame(index: int, pict_type: str, pts: int | None, dts: int | None = None) -> FrameRecord:
    return FrameRecord(
        index=index,
        pict_type=pict_type,
        pts=pts,
        dts=dts if dts is not None else pts,
        time_base=TB,
    )


def ordered_gop() -> list[FrameRecord]:
    """Frames already in presentation order: I B B P."""
    return [
        frame(0, "I", 0),
        frame(1, "B", 1),
        frame(2, "B", 2),
        frame(3, "P", 3),
    ]


def out_of_order_gop() -> list[FrameRecord]:
    """Decoder order I P B B with timestamps that place the B frames first."""
    return [
        frame(0, "I", 0),
        frame(1, "P", 3),
        frame(2, "B", 1),
        frame(3, "B", 2),
    ]


def values(data) -> list[int | None]:
    return [f.repaired_pts for f in data.frames]


def types(data) -> list[str]:
    return [f.pict_type for f in data.frames]


# ---------------------------------------------------------------- step model


def test_step_uses_sorted_deltas_not_decode_order() -> None:
    # Arrival order is jagged (B frames), but the underlying cadence is 3 ticks.
    assert derive_step_ticks([0, 9, 3, 6, 15, 12], time_base=TB, average_rate=None) == 3


def test_step_ignores_extreme_gaps() -> None:
    assert derive_step_ticks([0, 3, 6, 900, 9, 12], time_base=TB, average_rate=None) == 3


def test_step_falls_back_to_declared_rate() -> None:
    rate = Fraction(24)
    assert derive_step_ticks([], time_base=Fraction(1, 24), average_rate=rate) == 1
    assert derive_step_ticks([5], time_base=Fraction(1, 12_288), average_rate=rate) == 512


def test_step_never_zero() -> None:
    assert derive_step_ticks([], time_base=None, average_rate=None) == 1
    assert derive_step_ticks([7, 7, 7], time_base=None, average_rate=None) == 1


# ------------------------------------------------------------ frame ordering


def test_clean_stream_is_not_reordered_or_repaired() -> None:
    data = build_timeline(ordered_gop(), time_base=TB, average_rate=Fraction(30))
    assert data.reordered_frames == 0
    assert data.repairs == 0
    assert data.anomalies == 0
    assert values(data) == [0, 1, 2, 3]
    assert all(f.kind is RepairKind.NONE for f in data.frames)


def test_b_frames_are_reordered_into_presentation_order() -> None:
    data = build_timeline(out_of_order_gop(), time_base=TB, average_rate=Fraction(30))
    assert types(data) == ["I", "B", "B", "P"]
    assert values(data) == [0, 1, 2, 3]
    assert data.reordered_frames == 3


def test_ordering_is_stable_for_equal_keys() -> None:
    frames = [frame(0, "I", 0), frame(1, "B", 0), frame(2, "P", 2)]
    assert presentation_order(frames, step=1) == [0, 1, 2]


def test_missing_timestamp_stays_in_its_emission_slot() -> None:
    frames = [frame(0, "I", 0), frame(1, "B", None), frame(2, "B", 2)]
    keys = effective_keys(frames, step=2)
    assert list(keys) == [0, Fraction(1), 2]
    assert presentation_order(frames, step=2) == [0, 1, 2]


def test_missing_leading_timestamp_extrapolates_backwards() -> None:
    frames = [frame(0, "I", None), frame(1, "P", 5), frame(2, "B", 6)]
    keys = effective_keys(frames, step=1)
    assert keys[0] < keys[1]
    assert presentation_order(frames, step=1) == [0, 1, 2]


# --------------------------------------------------------------- repair pass


def test_missing_timestamps_are_filled_and_counted() -> None:
    frames = [frame(0, "I", 0), frame(1, "B", None), frame(2, "B", None), frame(3, "P", 3)]
    data = build_timeline(frames, time_base=TB, average_rate=Fraction(30))
    result = [v for v in values(data) if v is not None]
    assert all(b > a for a, b in pairwise(result))
    assert data.breakdown["missing"] == 2
    assert data.repairs == 2


def test_duplicate_timestamp_is_pushed_forward() -> None:
    frames = [frame(0, "I", 0), frame(1, "B", 1), frame(2, "B", 1), frame(3, "P", 3)]
    data = build_timeline(frames, time_base=TB)
    result = [v for v in values(data) if v is not None]
    assert result == [0, 1, 2, 3]
    assert data.breakdown["duplicate"] == 1
    assert data.repairs == 1


def test_regression_is_repaired_monotonically() -> None:
    # The B frame has a PTS far past its neighbours: after sorting it lands last
    # and its timestamp has to move, which the repair pass must count.
    frames = [frame(0, "I", 0), frame(1, "B", 1), frame(2, "B", 9), frame(3, "P", 3)]
    data = build_timeline(frames, time_base=TB)
    result = [v for v in values(data) if v is not None]
    assert all(b > a for a, b in pairwise(result))
    assert data.repairs >= 1
    assert data.breakdown["regression"] + data.breakdown["duplicate"] >= 1


def test_first_frame_negative_is_a_repair() -> None:
    frames = [frame(0, "I", -5), frame(1, "B", 1), frame(2, "P", 2)]
    data = build_timeline(frames, time_base=TB)
    first = data.frames[0]
    assert first.kind is RepairKind.FIRST_FRAME
    assert first.original_pts == -5
    assert first.repaired_pts == 0
    assert data.repairs == 1
    assert data.breakdown["first_frame"] == 1


def test_first_frame_missing_is_a_repair() -> None:
    frames = [frame(0, "I", None), frame(1, "P", 2), frame(2, "B", 1)]
    data = build_timeline(frames, time_base=TB)
    assert data.breakdown["first_frame"] == 1
    result = [v for v in values(data) if v is not None]
    assert all(b > a for a, b in pairwise(result))


def test_all_missing_timestamps_still_produce_a_timeline() -> None:
    frames = [frame(i, "P", None) for i in range(4)]
    data = build_timeline(frames, time_base=TB, average_rate=Fraction(24))
    result = [v for v in values(data) if v is not None]
    assert result == [0, 3750, 7500, 11250]
    assert data.anomalies == 4


def test_repairs_never_emit_a_decreasing_timeline() -> None:
    frames = [
        frame(0, "I", 0),
        frame(1, "P", 5),
        frame(2, "B", 5),
        frame(3, "B", 1),
        frame(4, "P", None),
        frame(5, "B", 2),
    ]
    data = build_timeline(frames, time_base=TB)
    result = [v for v in values(data) if v is not None]
    assert all(b > a for a, b in pairwise(result))
    assert data.repairs >= 3


# ---------------------------------------------------------- empty and policies


def test_empty_input_yields_empty_timeline() -> None:
    data = build_timeline([], time_base=TB)
    assert data.count == 0
    assert data.repairs == 0
    assert data.max_jump_us == 0
    assert data.reordered_frames == 0


def test_single_frame_timeline() -> None:
    data = build_timeline([frame(0, "I", 10)], time_base=TB)
    assert data.count == 1
    assert data.repairs == 0
    assert data.max_jump_us == 0


def test_report_policy_detects_without_rewriting() -> None:
    frames = [frame(0, "I", 0), frame(1, "B", None), frame(2, "B", 2), frame(3, "P", 3)]
    data = build_timeline(frames, time_base=TB, policy=Policy.REPORT)
    assert data.applied is False
    assert data.anomalies == 1
    assert data.repairs == 0
    missing = [f for f in data.frames if f.kind is RepairKind.MISSING]
    assert len(missing) == 1
    assert missing[0].applied is False
    assert data.frames[1].repaired_pts is None


def test_max_jump_is_reported_in_microseconds() -> None:
    frames = [
        frame(0, "I", 0),
        frame(1, "P", 90_000),  # one second
        frame(2, "B", 90_000),  # must be repaired
    ]
    data = build_timeline(frames, time_base=TB)
    assert data.max_jump_us >= 1_000_000


@pytest.mark.parametrize("policy", [Policy.REPAIR, Policy.REPORT])
def test_counts_cover_every_decoded_frame(policy: Policy) -> None:
    frames = [frame(0, "I", 0), frame(1, "B", None), frame(2, "B", 2), frame(3, "P", 3)]
    data = build_timeline(frames, time_base=TB, policy=policy)
    assert data.count == len(frames)
    assert data.count_type("I") + data.count_type("P") + data.count_type("B") == len(frames)
