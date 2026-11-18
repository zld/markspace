"""End-to-end and error-path tests for the cutline pipeline."""

from __future__ import annotations

import json
from fractions import Fraction
from pathlib import Path

import pytest

from cutline.model import Clip, ModelError, Timeline, Track
from cutline.rational import RationalTime, Span, TimeError, parse_rate, round_half_away
from cutline.conflicts import ConflictKind, detect


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
