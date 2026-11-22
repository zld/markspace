"""Tests for frame rounding, conflict detection and resolution rules."""

from __future__ import annotations

from fractions import Fraction

import pytest

from timeline_core.model import (
    Clip,
    FrameInterval,
    ResolutionError,
    Timeline,
    Track,
    frames_from_seconds,
    union_covered_frames,
)
from timeline_core.resolve import detect_conflicts, resolve_timeline

FPS = 24


def clip(clip_id: str, start: int, end: int, priority: int, track: str = "V") -> Clip:
    return Clip(clip_id, FrameInterval(start, end), priority, track)


def demo_timeline() -> Timeline:
    # A 0-5s p1, B 3-7s p2, C 6-9s p1 (the acceptance demo delivery).
    return Timeline(
        FPS,
        (
            Track.from_clips(
                "V1",
                (clip("A", 0, 120, 1), clip("C", 144, 216, 1)),
            ),
            Track.from_clips("V2", (clip("B", 72, 168, 2),)),
        ),
    )


# ---------------------------------------------------------------- rounding


def test_seconds_to_integer_frames_exact():
    assert frames_from_seconds(0, FPS) == 0
    assert frames_from_seconds(3, FPS) == 72
    assert frames_from_seconds(7, FPS) == 168
    assert frames_from_seconds("9", FPS) == 216


def test_rounding_nearest_frame():
    # 0.6 frames expressed in seconds -> frame 1; 0.4 frames -> frame 0.
    assert frames_from_seconds(Fraction(6, 10) / FPS, FPS) == 1
    assert frames_from_seconds(Fraction(4, 10) / FPS, FPS) == 0
    # Decimal input via string stays exact: 1.041(6)s = 25.0 frames...
    assert frames_from_seconds("1.0", FPS) == 24


def test_half_frame_tie_goes_to_earlier_frame():
    half = Fraction(1, 2 * FPS)  # exactly half of one frame in seconds
    assert frames_from_seconds(half, FPS) == 0
    assert frames_from_seconds(Fraction(3, 2 * FPS), FPS) == 1
    assert frames_from_seconds(Fraction(5, 2 * FPS), FPS) == 2


def test_half_frame_tie_negative_goes_earlier_toward_minus_infinity():
    # -0.5 frames must round toward -1 (earlier), not toward zero.
    assert frames_from_seconds(Fraction(-1, 2 * FPS), FPS) == -1


def test_invalid_fps_rejected():
    with pytest.raises(ResolutionError):
        frames_from_seconds(1, 0)
    with pytest.raises(ResolutionError):
        frames_from_seconds(1, -24)


def test_invalid_time_value_rejected():
    with pytest.raises(ResolutionError):
        frames_from_seconds("not-a-number", FPS)


# ------------------------------------------------------------- intervals


def test_half_open_intervals_touching_do_not_overlap():
    assert FrameInterval(0, 72).overlaps(FrameInterval(72, 144)) is False
    assert FrameInterval(0, 72).intersection(FrameInterval(72, 144)) is None


def test_invalid_intervals_rejected():
    with pytest.raises(ResolutionError):
        FrameInterval(5, 4)
    with pytest.raises(ResolutionError):
        FrameInterval(-1, 4)


def test_union_coverage_counts_distinct_frames_not_clip_sums():
    intervals = [FrameInterval(0, 120), FrameInterval(72, 168), FrameInterval(144, 216)]
    # Clip-duration sum would be 120+96+72 = 288; union coverage is 216.
    assert union_covered_frames(intervals) == 216
    assert union_covered_frames([FrameInterval(10, 10)]) == 0


# ------------------------------------------------------------- conflicts


def test_demo_conflicts_counted_per_overlapping_pair():
    result = resolve_timeline(demo_timeline())
    assert result.conflict_count == 2
    pairs = sorted((c.first_id, c.second_id) for c in result.conflicts)
    assert pairs == [("A", "B"), ("B", "C")]


def test_touching_clips_produce_no_conflict():
    timeline = Timeline.from_clips(FPS, [clip("A", 0, 72, 1), clip("B", 72, 144, 1)])
    assert detect_conflicts(list(timeline.clips)) == ()


# ------------------------------------------------------------ resolution


def test_demo_resolved_intervals_match_acceptance():
    result = resolve_timeline(demo_timeline())
    assert [
        (i.start, i.end, i.clip_id) for i in result.intervals
    ] == [(0, 72, "A"), (72, 168, "B"), (168, 216, "C")]
    assert result.duration_deviation_frames == 0
    assert result.covered_frames_before == 216
    assert result.covered_frames_after == 216


def test_highest_priority_wins_every_frame():
    low = clip("LOW", 0, 100, 1)
    high = clip("HIGH", 20, 80, 5)
    result = resolve_timeline(Timeline.from_clips(FPS, [low, high]))
    assert [
        (i.start, i.end, i.clip_id) for i in result.intervals
    ] == [(0, 20, "LOW"), (20, 80, "HIGH"), (80, 100, "LOW")]


def test_equal_priority_resolved_by_lexicographic_id():
    timeline = Timeline.from_clips(
        FPS, [clip("B", 0, 96, 1), clip("A", 48, 144, 1)]
    )
    result = resolve_timeline(timeline)
    assert [
        (i.start, i.end, i.clip_id) for i in result.intervals
    ] == [(0, 48, "B"), (48, 144, "A")]


def test_resolution_independent_of_record_order():
    clips = [
        clip("A", 0, 120, 1),
        clip("B", 72, 168, 2),
        clip("C", 144, 216, 1),
    ]
    expected = [
        (i.start, i.end, i.clip_id)
        for i in resolve_timeline(Timeline.from_clips(FPS, clips)).intervals
    ]
    for ordering in (reversed(clips), [clips[2], clips[0], clips[1]]):
        result = resolve_timeline(Timeline.from_clips(FPS, ordering))
        assert [
            (i.start, i.end, i.clip_id) for i in result.intervals
        ] == expected


def test_adjacent_same_owner_spans_merge():
    # HIGH wins two disjoint windows inside LOW; the two LOW remnants must
    # stay separate while HIGH's single window stays one interval.
    low = clip("LOW", 0, 100, 0)
    high_a = clip("HIGH", 20, 40, 9)
    high_b = clip("HIGH", 60, 80, 9)  # same stable id, contiguous ownership
    timeline = Timeline.from_clips(FPS, [low, high_a, high_b])
    with pytest.raises(ResolutionError):
        timeline.validate_unique_ids()
    # Distinct ids but same priority class still merge when one owner repeats.
    high_b = clip("H2", 60, 80, 9)
    result = resolve_timeline(Timeline.from_clips(FPS, [low, high_a, high_b]))
    owners = [(i.start, i.end, i.clip_id) for i in result.intervals]
    assert owners == [
        (0, 20, "LOW"),
        (20, 40, "HIGH"),
        (40, 60, "LOW"),
        (60, 80, "H2"),
        (80, 100, "LOW"),
    ]


def test_gaps_remain_uncovered_and_unowned():
    timeline = Timeline.from_clips(FPS, [clip("A", 0, 10, 1), clip("B", 20, 30, 1)])
    result = resolve_timeline(timeline)
    assert result.covered_frames_after == 20
    assert [(i.start, i.end) for i in result.intervals] == [(0, 10), (20, 30)]


def test_duplicate_ids_rejected():
    timeline = Timeline.from_clips(
        FPS, [clip("A", 0, 10, 1), clip("A", 5, 15, 1)]
    )
    with pytest.raises(ResolutionError, match="duplicate clip ids"):
        resolve_timeline(timeline)


def test_non_positive_duration_clip_rejected():
    timeline = Timeline.from_clips(FPS, [clip("A", 10, 10, 1)])
    with pytest.raises(ResolutionError, match="non-positive duration"):
        resolve_timeline(timeline)
