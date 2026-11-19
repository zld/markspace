"""Timebase, model, overlap and resolution edge cases."""

from __future__ import annotations

from fractions import Fraction

import pytest

from cutline.errors import ModelError, ResolutionError, TimebaseError
from cutline.model import Clip, RateSegment, Sequence, Track
from cutline.overlap import scan_sequence, scan_track
from cutline.resolve import resolve, slice_clip
from cutline.timebase import FrameRate, FrameSpan, as_fraction, require_int


# --------------------------------------------------------------------------
# Timebase


def test_rational_rate_reduces_and_converts_exactly():
    rate = FrameRate(60000, 2002)
    assert (rate.numerator, rate.denominator) == (30000, 1001)
    assert rate.frames_to_seconds(30000) == Fraction(1001)
    assert rate.frames_from_seconds(Fraction(1001)) == 30000
    assert rate.seconds_to_frames_exact(Fraction(1, 2)) == Fraction(15000, 1001)


def test_timebase_rejects_zero_negative_and_non_finite():
    with pytest.raises(TimebaseError):
        FrameRate(0, 1)
    with pytest.raises(TimebaseError):
        FrameRate(24, 0)
    with pytest.raises(TimebaseError):
        FrameRate(-24, 1)
    for bad in (float("inf"), float("-inf"), float("nan")):
        with pytest.raises(TimebaseError):
            as_fraction(bad, "seconds")


def test_timebase_rejects_off_frame_boundaries():
    rate = FrameRate(24)
    with pytest.raises(TimebaseError, match="frame boundary"):
        rate.frames_from_seconds(Fraction(1, 10))
    assert rate.frames_from_seconds(Fraction(1, 8)) == 3


def test_require_int_rejects_booleans_and_floats():
    with pytest.raises(TimebaseError):
        require_int(True, "x")
    with pytest.raises(TimebaseError):
        require_int(1.0, "x", minimum=0)
    with pytest.raises(TimebaseError):
        require_int(-1, "x", minimum=0)


def test_half_open_spans_and_adjacency():
    a, b = FrameSpan(0, 10), FrameSpan(10, 5)
    assert not a.overlaps(b)
    assert a.is_adjacent(b)
    assert FrameSpan(0, 10).overlap_frames(FrameSpan(5, 10)) == 5
    assert FrameSpan(0, 10).intersection(FrameSpan(10, 1)) is None


# --------------------------------------------------------------------------
# Model


def test_clip_validates_source_range_speed_and_fades():
    with pytest.raises(ModelError, match="exceeds media"):
        Clip(id="c", media_id="m", timeline_start=0, duration=10,
             source_in=5, source_duration=10)
    with pytest.raises(ModelError, match="must be > 0"):
        RateSegment(0, 5, Fraction(0))
    with pytest.raises(ModelError, match="not a whole frame"):
        Clip(id="c", media_id="m", timeline_start=0, duration=5,
             source_in=0, source_duration=30,
             rate_segments=(RateSegment(0, 5, Fraction(3, 2)),))
    with pytest.raises(ModelError, match="exceed clip duration"):
        Clip(id="c", media_id="m", timeline_start=0, duration=4,
             source_in=0, source_duration=4, fade_in=3, fade_out=3)


def test_rate_segments_must_tile_and_map_whole_source_frames():
    with pytest.raises(ModelError, match="consume"):
        RateSegment(0, 5, Fraction(1, 2))
    clip = Clip(
        id="ramp", media_id="m", timeline_start=0, duration=10,
        source_in=0, source_duration=30,
        rate_segments=(RateSegment(0, 5, Fraction(2)), RateSegment(5, 5, Fraction(1))),
    )
    assert clip.source_frames == 15
    assert clip.source_offset_at(0) == 0
    assert clip.source_offset_at(5) == 10
    assert clip.source_offset_at(9) == 14
    assert not clip.is_constant_speed
    with pytest.raises(ModelError, match="tile"):
        Clip(id="bad", media_id="m", timeline_start=0, duration=10,
             source_in=0, source_duration=30,
             rate_segments=(RateSegment(1, 5, Fraction(1)),))


def test_duplicate_ids_are_rejected():
    clip = Clip(id="c", media_id="m", timeline_start=0, duration=4,
                source_in=0, source_duration=4)
    with pytest.raises(ModelError, match="duplicate clip id"):
        Track(id="t", clips=(clip, clip))
    with pytest.raises(ModelError, match="duplicate clip id"):
        Sequence(id="s", frame_rate=FrameRate(24), tracks=(
            Track(id="t1", clips=(clip,)),
            Track(id="t2", clips=(clip,)),
        ))


# --------------------------------------------------------------------------
# Overlap


def _seq(*clips: Clip, tracks: int = 1) -> Sequence:
    return Sequence(id="s", frame_rate=FrameRate(24), tracks=(Track(id="t", clips=clips),))


def test_touching_clips_do_not_conflict():
    a = Clip(id="a", media_id="m", timeline_start=0, duration=10, source_in=0, source_duration=10)
    b = Clip(id="b", media_id="m", timeline_start=10, duration=10, source_in=0, source_duration=10)
    assert scan_sequence(_seq(a, b)).conflict_count == 0


def test_overlap_counts_frames_and_ignores_other_tracks():
    a = Clip(id="a", media_id="m", timeline_start=0, duration=10, source_in=0, source_duration=10)
    b = Clip(id="b", media_id="m", timeline_start=4, duration=10, source_in=0, source_duration=10)
    report = scan_sequence(_seq(a, b))
    assert report.conflict_count == 1
    assert report.conflicts[0].overlap_frames == 6
    assert report.conflicts[0].overlap_span == FrameSpan(4, 6)

    cross = Sequence(id="s", frame_rate=FrameRate(24), tracks=(
        Track(id="t1", clips=(a,)), Track(id="t2", clips=(b,)),
    ))
    assert scan_sequence(cross).conflict_count == 0


def test_three_way_overlap_reports_all_pairs():
    clips = [
        Clip(id=name, media_id="m", timeline_start=start, duration=10,
             source_in=0, source_duration=10)
        for name, start in (("a", 0), ("b", 5), ("c", 8))
    ]
    report = scan_sequence(_seq(*clips))
    assert report.conflict_count == 3
    assert report.total_overlap_frames == 5 + 7 + 2
    assert [c.key for c in report.conflicts] == sorted(c.key for c in report.conflicts)


def test_scan_uses_sweep_not_pairwise():
    # A sample sweep of 100k disjoint clips must not degrade quadratically.
    clips = tuple(
        Clip(id=f"c{i}", media_id="m", timeline_start=i * 2, duration=1,
             source_in=0, source_duration=1)
        for i in range(20000)
    )
    report = scan_sequence(Sequence(id="big", frame_rate=FrameRate(24),
                                    tracks=(Track(id="t", clips=clips),)))
    assert report.conflict_count == 0


# --------------------------------------------------------------------------
# Resolution


def test_priority_trim_low_priority_and_keep_fades_valid():
    low = Clip(id="low", media_id="m", timeline_start=0, duration=20,
               source_in=0, source_duration=20, fade_in=4, fade_out=4)
    high = Clip(id="high", media_id="m", timeline_start=10, duration=10,
                source_in=0, source_duration=20, priority=5)
    result = resolve(_seq(low, high), "priority")
    assert result.conflicts_before == 1
    assert result.conflicts_after == 0
    intervals = {i.clip_id: i for i in result.intervals}
    assert intervals["low"].timeline_start == 0 and intervals["low"].duration == 10
    assert intervals["low"].fade_in == 4
    assert intervals["low"].fade_out == 0
    assert intervals["high"].duration == 10
    assert result.duration_delta_frames == 0


def test_priority_splits_clip_covered_on_both_sides():
    centre = Clip(id="centre", media_id="m", timeline_start=10, duration=20,
                  source_in=0, source_duration=40)
    left = Clip(id="left", media_id="m", timeline_start=0, duration=15,
                source_in=0, source_duration=20, priority=2)
    right = Clip(id="right", media_id="m", timeline_start=25, duration=15,
                 source_in=0, source_duration=20, priority=2)
    result = resolve(_seq(centre, left, right), "priority")
    placed = [(i.clip_id, i.timeline_start, i.duration, i.source_in)
              for i in result.intervals]
    # The centre clip keeps a leading fragment at 15..25 and a tail fragment
    # from 25..30; the tail shares its origin id with an ordinal suffix.
    assert placed == [("left", 0, 15, 0), ("centre", 15, 10, 5), ("right", 25, 15, 0)]
    assert result.conflicts_after == 0


def test_priority_ties_follow_original_order_then_id():
    first = Clip(id="z", media_id="m", timeline_start=0, duration=10,
                 source_in=0, source_duration=10)
    second = Clip(id="a", media_id="m", timeline_start=5, duration=10,
                  source_in=0, source_duration=10)
    result = resolve(_seq(first, second), "priority")
    starts = {i.clip_id: (i.timeline_start, i.duration) for i in result.intervals}
    assert starts["z"] == (0, 10) and starts["a"] == (10, 5)


def test_preserve_duration_keeps_lengths_and_shifts_forward():
    a = Clip(id="a", media_id="m", timeline_start=0, duration=20, source_in=0, source_duration=20)
    b = Clip(id="b", media_id="m", timeline_start=10, duration=10, source_in=0, source_duration=10)
    c = Clip(id="c", media_id="m", timeline_start=12, duration=5, source_in=0, source_duration=5)
    result = resolve(_seq(a, b, c), "preserve-duration")
    placed = [(i.clip_id, i.timeline_start, i.duration) for i in result.intervals]
    assert placed == [("a", 0, 20), ("b", 20, 10), ("c", 30, 5)]
    assert result.duration_delta_frames == 35 - 20
    assert result.conflicts_after == 0


def test_resolution_is_deterministic(sample_sequence):
    for strategy in ("priority", "preserve-duration"):
        first = resolve(sample_sequence, strategy)
        second = resolve(sample_sequence, strategy)
        assert first.to_json() == second.to_json()


def test_slice_between_source_frames_is_rejected():
    ramp = Clip(id="ramp", media_id="m", timeline_start=0, duration=10,
                source_in=0, source_duration=30,
                rate_segments=(RateSegment(0, 4, Fraction(3, 2)), RateSegment(4, 6, Fraction(1))))
    with pytest.raises(ResolutionError, match="between source frames"):
        slice_clip(ramp, 1, 6)  # local frame 1 lands on 3/2 source frames
    head = slice_clip(ramp, 0, 2)
    assert (head.duration, head.source_in, head.source_frames) == (2, 0, 3)


def test_unknown_strategy_and_impossible_resolution_are_located():
    with pytest.raises(ResolutionError, match="unknown strategy"):
        resolve(_seq(), "nonsense")
    with pytest.raises(ModelError):
        Clip(id="infeasible", media_id="m", timeline_start=0, duration=10,
             source_in=9, source_duration=10)
