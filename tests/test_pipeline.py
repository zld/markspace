"""End-to-end and error-path tests for the cutline pipeline."""

from __future__ import annotations

import json
from fractions import Fraction
from pathlib import Path

import pytest

from cutline.model import Clip, ModelError, Timeline, Track
from cutline.rational import RationalTime, Span, TimeError, parse_rate, round_half_away
from cutline.conflicts import ConflictKind, detect
from cutline.resolve import DecisionAction, Strategy, resolve


def _clip(clip_id: str, start: str, duration: str, priority: int = 0, rate: str = "24") -> Clip:
    return Clip(
        id=clip_id,
        start=RationalTime(Fraction(start)),
        duration=RationalTime(Fraction(duration)),
        rate=parse_rate(rate),
        priority=priority,
    )


def _track(track_id: str, *clips: Clip) -> Track:
    return Track(id=track_id, clips=list(clips), rate=Fraction(24))


def _snapshot(timeline: Timeline) -> list:
    return [
        (
            track.id,
            [
                (clip.id, clip.start.as_seconds_text(), clip.duration.as_seconds_text())
                for clip in track.clips
            ],
        )
        for track in timeline.tracks
    ]


def test_rational_converts_ntsc_rates_exactly():
    rate = parse_rate("24000/1001")
    frames = RationalTime.from_frames(24000, rate)
    assert frames.seconds == Fraction(1001)
    assert frames.seconds * rate == 24000
    # Ten thousand repeated conversions must not drift.
    total = Fraction(0)
    for _ in range(10_000):
        total += frames.seconds
    assert total == Fraction(1001) * 10_000


def test_round_half_away_is_symmetric():
    assert round_half_away(Fraction(5, 2)) == 3
    assert round_half_away(Fraction(-5, 2)) == -3
    assert round_half_away(Fraction(3, 2)) == 2
    assert round_half_away(Fraction(-3, 2)) == -2


@pytest.mark.parametrize("bad", ["0", "-24", "abc", "", "1/0"])


def test_invalid_rates_are_located(bad: str):
    with pytest.raises(TimeError) as info:
        parse_rate(bad, "tracks[0].rate")
    assert "tracks[0].rate" in str(info.value)


def test_float_rates_are_rejected():
    with pytest.raises(TimeError):
        parse_rate(23.976, "clips[0].rate")


def test_span_touching_is_not_overlap():
    a = Span(RationalTime(Fraction(0)), RationalTime(Fraction(2)))
    b = Span(RationalTime(Fraction(2)), RationalTime(Fraction(4)))
    assert not a.overlaps(b)
    assert a.touches(b)
    assert a.intersection(b) is None


# ---------------------------------------------------------------------------
# model validation
# ---------------------------------------------------------------------------


def test_zero_duration_is_rejected():
    payload = {
        "version": "v2",
        "timeline": {
            "tracks": [
                {"id": "a", "rate": "24", "clips": [{"id": "c1", "start": "1", "duration": "0"}]}
            ]
        },
    }
    with pytest.raises(ModelError) as info:
        __import__("cutline.model", fromlist=["build_timeline"]).build_timeline(payload["timeline"])
    assert "duration must be positive" in str(info.value)


# ---------------------------------------------------------------------------
# conflict detection
# ---------------------------------------------------------------------------


def test_detection_separates_kinds():
    timeline = Timeline(
        id="t",
        tracks=[
            _track("v1", _clip("a", "0", "4"), _clip("b", "2", "4"), _clip("c", "6", "2")),
            _track("v2", _clip("x", "3", "2"), _clip("y", "8", "1")),
        ],
    )
    report = detect(timeline)
    kinds = [conflict.kind for conflict in report.conflicts]
    assert kinds.count(ConflictKind.SAME_TRACK_OVERLAP) == 1
    # a and b both overlap x on v2: two cross-track conflicts, not one.
    assert kinds.count(ConflictKind.CROSS_TRACK_OVERLAP) == 2
    overlap = next(c for c in report.conflicts if c.kind is ConflictKind.SAME_TRACK_OVERLAP)
    assert overlap.intersection.as_text() == {"start": "2", "end": "4"}
    # b ends at 6 and c starts at 6 -> adjacency, not a conflict.
    assert any(item.clip_b == "c" for item in report.adjacent)
    crossing = [c for c in report.conflicts if c.kind is ConflictKind.CROSS_TRACK_OVERLAP]
    assert {c.clip_a for c in crossing} == {"a", "b"}
    assert all(c.track_b == "v2" for c in crossing)


def test_detection_is_stable_across_input_order():
    clips = [_clip("a", "0", "4"), _clip("b", "1", "4"), _clip("c", "2", "4")]
    forward = detect(Timeline(id="t", tracks=[_track("v1", *clips)])).conflicts
    shuffled = detect(Timeline(id="t", tracks=[_track("v1", *reversed(clips))])).conflicts
    assert [c.as_dict() for c in forward] == [c.as_dict() for c in shuffled]


# ---------------------------------------------------------------------------
# resolution
# ---------------------------------------------------------------------------


def test_priority_trim_keeps_stronger_clip():
    timeline = Timeline(
        id="t", tracks=[_track("v1", _clip("low", "0", "4", priority=1), _clip("high", "2", "4", priority=9))]
    )
    result = resolve(timeline, Strategy.PRIORITY)
    assert result.unresolved == []
    kept = {clip.id: clip for clip in result.timeline.tracks[0].clips}
    assert kept["low"].span.as_text() == {"start": "0", "end": "2"}
    assert kept["high"].span.as_text() == {"start": "2", "end": "6"}


def test_equal_priority_breaks_ties_by_id():
    timeline = Timeline(id="t", tracks=[_track("v1", _clip("bbb", "0", "4"), _clip("aaa", "2", "4"))])
    result = resolve(timeline, Strategy.PRIORITY)
    kept = {clip.id: clip for clip in result.timeline.tracks[0].clips}
    assert kept["bbb"].span.as_text() == {"start": "0", "end": "2"}
    assert kept["aaa"].span.as_text() == {"start": "2", "end": "6"}


def test_fully_covered_clip_is_removed_with_reason():
    timeline = Timeline(id="t", tracks=[_track("v1", _clip("long", "0", "6"), _clip("short", "2", "2"))])
    result = resolve(timeline, Strategy.PRIORITY)
    assert [clip.id for clip in result.timeline.tracks[0].clips] == ["long"]
    removed = [d for d in result.decisions if d.action is DecisionAction.REMOVED]
    assert removed and removed[0].clip_id == "short"
    assert "fully covered" in removed[0].reason


def test_shift_moves_later_clip_left_into_free_space():
    timeline = Timeline(id="t", tracks=[_track("v1", _clip("a", "5", "2"), _clip("b", "6", "2"))])
    result = resolve(timeline, Strategy.SHIFT)
    assert result.unresolved == []
    kept = {clip.id: clip for clip in result.timeline.tracks[0].clips}
    assert kept["b"].span.as_text() == {"start": "3", "end": "5"}
    assert any(d.action is DecisionAction.SHIFTED for d in result.decisions)


def test_shift_reports_clip_that_cannot_fit():
    timeline = Timeline(id="t", tracks=[_track("v1", _clip("a", "0", "2"), _clip("b", "1", "2"))])
    result = resolve(timeline, Strategy.SHIFT)
    assert result.has_unresolved


def test_reject_strategy_changes_nothing():
    timeline = Timeline(id="t", tracks=[_track("v1", _clip("a", "0", "4"), _clip("b", "2", "4"))])
    result = resolve(timeline, Strategy.REJECT)
    assert result.has_unresolved
    assert result.timeline.tracks[0].clips[0].span.as_text() == {"start": "0", "end": "4"}


def test_resolution_is_deterministic():
    timeline = Timeline(
        id="t",
        tracks=[
            _track("v1", _clip("a", "0", "5"), _clip("b", "1", "5"), _clip("c", "3", "5")),
            _track("v2", _clip("x", "2", "4"), _clip("y", "5", "2")),
        ],
    )
    first = resolve(timeline, Strategy.PRIORITY)
    second = resolve(timeline, Strategy.PRIORITY)
    assert [d.as_dict() for d in first.decisions] == [d.as_dict() for d in second.decisions]
    assert _snapshot(first.timeline) == _snapshot(second.timeline)


# ---------------------------------------------------------------------------
# render plan and summary
# ---------------------------------------------------------------------------
