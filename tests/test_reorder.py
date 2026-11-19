"""Tests for the bounded presentation reorder buffer."""

from __future__ import annotations

from fractions import Fraction

import pytest

from frameaudit.errors import LimitExceededError
from frameaudit.model import FrameRecord
from frameaudit.reorder import (
    ReorderBuffer,
    reorder_frames,
    stable_presentation_order,
)
from .conftest import make_record

TB = Fraction(1, 1000)


def coded_b_frame_sequence() -> list[FrameRecord]:
    """I, P(anchor), B, B: decode order differs from presentation order."""
    return [
        make_record(0, 0, frame_type="I", key_frame=True),
        make_record(1, 3000, frame_type="P"),
        make_record(2, 1000, frame_type="B"),
        make_record(3, 2000, frame_type="B"),
    ]


def test_reorders_b_frame_sequence_into_presentation_order():
    frames = coded_b_frame_sequence()
    ordered, overflows = reorder_frames(frames, window_size=8)
    assert [f.pts for f in ordered] == [0, 1000, 2000, 3000]
    assert overflows == 0


def test_decode_and_presentation_orders_differ():
    frames = coded_b_frame_sequence()
    ordered, _ = reorder_frames(frames, window_size=8)
    assert [f.decode_index for f in frames] != [
        f.decode_index for f in ordered
    ]


def test_duplicate_pts_keeps_decode_order_stably():
    frames = [
        make_record(0, 100),
        make_record(1, 100),
        make_record(2, 200),
    ]
    ordered, overflows = reorder_frames(frames, window_size=8)
    assert [f.decode_index for f in ordered] == [0, 1, 2]
    assert overflows == 0


def test_missing_pts_sorts_last_and_is_never_dropped():
    frames = [
        make_record(0, 200),
        make_record(1, None),
        make_record(2, 100),
    ]
    ordered, overflows = reorder_frames(frames, window_size=8)
    assert [f.decode_index for f in ordered] == [2, 0, 1]
    assert ordered[-1].pts is None
    assert len(ordered) == 3
    assert overflows == 0


def test_shallow_window_reports_overflow_without_losing_frames():
    # Frame with pts=50 arrives after the buffer already released larger keys.
    frames = [
        make_record(0, 0),
        make_record(1, 300),
        make_record(2, 100),
        make_record(3, 400),
        make_record(4, 50),
    ]
    ordered, overflows = reorder_frames(frames, window_size=2)
    assert len(ordered) == 5
    assert overflows >= 1
    # The late frame is still present, appended deterministically at the tail.
    assert {f.decode_index for f in ordered} == {0, 1, 2, 3, 4}


def test_adequate_window_produces_zero_overflows():
    frames = coded_b_frame_sequence()
    _, overflows = reorder_frames(frames, window_size=16)
    assert overflows == 0


def test_large_window_matches_pure_stable_sort():
    frames = coded_b_frame_sequence() + [
        make_record(4, None),
        make_record(5, 3000),
    ]
    buffered, _ = reorder_frames(frames, window_size=10_000)
    pure = stable_presentation_order(frames)
    assert [f.decode_index for f in buffered] == [
        f.decode_index for f in pure
    ]


def test_invalid_window_size_rejected():
    with pytest.raises(LimitExceededError):
        ReorderBuffer(window_size=0)


def test_max_frames_cap_enforced():
    frames = [make_record(i, i * 1000) for i in range(6)]
    with pytest.raises(LimitExceededError):
        reorder_frames(frames, window_size=2, max_frames=3)


def test_real_bframe_fixture_reorders_losslessly(fixture_video):
    from frameaudit.decode import decode_frames

    records, _ = decode_frames(fixture_video)
    assert any(r.frame_type == "B" for r in records)
    ordered, overflows = reorder_frames(records, window_size=16)
    assert len(ordered) == len(records)
    # A window wider than the GOP depth never overflows.
    assert overflows == 0
    pts = [r.pts_seconds() for r in ordered if r.pts is not None]
    assert all(pts[i] <= pts[i + 1] for i in range(len(pts) - 1))
