"""Tests for timestamp diagnosis and deterministic repair."""

from __future__ import annotations

from fractions import Fraction

import pytest

from frameaudit.errors import UsageError
from frameaudit.timestamps import (
    REASON_DUPLICATE,
    REASON_JUMP,
    REASON_MISSING,
    REASON_REGRESSION,
    frame_interval_seconds,
    parse_fraction,
    repair_timestamps,
)
from .conftest import make_record


def ordered_records(spec):
    """Build presentation-ordered records from (pts, ...) integer tuples."""
    return [make_record(i, pts) for i, pts in enumerate(spec)]


def test_clean_stream_is_not_modified():
    frames = ordered_records([0, 33, 66, 99])
    normalized, repaired, max_jump = repair_timestamps(
        frames, fps=Fraction(30), jump_threshold=2.0
    )
    assert repaired == 0
    assert all(not n.timestamp_repaired for n in normalized)
    assert max_jump == pytest.approx(0.033, abs=1e-3)


def test_vfr_spacing_is_preserved():
    # Irregular but legal deltas must keep their exact original timestamps.
    frames = ordered_records([0, 50, 150, 160])
    normalized, repaired, _ = repair_timestamps(
        frames, fps=Fraction(30), jump_threshold=2.0
    )
    assert repaired == 0
    assert [n.repaired_pts_seconds for n in normalized] == [0.0, 0.05, 0.15, 0.16]


def test_missing_timestamp_detected_and_filled():
    frames = ordered_records([0, 33, None, 99])
    normalized, repaired, _ = repair_timestamps(
        frames, fps=Fraction(30), jump_threshold=2.0
    )
    missing = [n for n in normalized if REASON_MISSING in n.repair_reasons]
    assert len(missing) == 1 and repaired == 1
    ts = [n.repaired_pts_seconds for n in normalized]
    assert all(ts[i] < ts[i + 1] for i in range(len(ts) - 1))


def test_duplicate_timestamp_detected():
    frames = ordered_records([0, 100, 100, 200])
    normalized, repaired, _ = repair_timestamps(
        frames, fps=Fraction(10), jump_threshold=2.0
    )
    dups = [n for n in normalized if REASON_DUPLICATE in n.repair_reasons]
    assert len(dups) == 1 and repaired == 1
    ts = [n.repaired_pts_seconds for n in normalized]
    assert all(ts[i] < ts[i + 1] for i in range(len(ts) - 1))


def test_regression_timestamp_detected():
    frames = ordered_records([0, 100, 50, 200])
    normalized, repaired, _ = repair_timestamps(
        frames, fps=Fraction(10), jump_threshold=2.0
    )
    regressions = [
        n for n in normalized if REASON_REGRESSION in n.repair_reasons
    ]
    assert len(regressions) == 1 and repaired == 1
    ts = [n.repaired_pts_seconds for n in normalized]
    assert all(ts[i] < ts[i + 1] for i in range(len(ts) - 1))


def test_jump_timestamp_detected_and_max_jump_reported():
    frames = ordered_records([0, 33, 5000])
    normalized, repaired, max_jump = repair_timestamps(
        frames, fps=Fraction(30), jump_threshold=2.0
    )
    jumps = [n for n in normalized if REASON_JUMP in n.repair_reasons]
    assert len(jumps) == 1 and repaired == 1
    assert max_jump == pytest.approx(4.967, abs=1e-3)


def test_all_four_fault_classes_in_one_stream():
    # Presentation-order deltas: dup (0), regression (-50), jump (+4900), missing
    frames = ordered_records([0, 100, 100, 50, 5000, None])
    normalized, repaired, max_jump = repair_timestamps(
        frames, fps=Fraction(30), jump_threshold=2.0
    )
    seen: set[str] = set()
    for n in normalized:
        seen.update(n.repair_reasons)
    assert {
        REASON_MISSING,
        REASON_DUPLICATE,
        REASON_REGRESSION,
        REASON_JUMP,
    } <= seen
    ts = [n.repaired_pts_seconds for n in normalized]
    assert all(ts[i] < ts[i + 1] for i in range(len(ts) - 1))
    assert max_jump > 2.0
    assert repaired == 4


def test_leading_missing_frames_back_filled_strictly():
    frames = ordered_records([None, None, 100, 200])
    normalized, repaired, _ = repair_timestamps(
        frames, fps=Fraction(10), jump_threshold=2.0
    )
    ts = [n.repaired_pts_seconds for n in normalized]
    assert all(ts[i] < ts[i + 1] for i in range(len(ts) - 1))
    assert all(n.timestamp_repaired for n in normalized[:2])
    assert repaired == 2


def test_all_missing_stream_synthesizes_clock():
    frames = ordered_records([None, None])
    normalized, repaired, _ = repair_timestamps(
        frames, fps=Fraction(10), jump_threshold=2.0
    )
    assert repaired == 2
    ts = [n.repaired_pts_seconds for n in normalized]
    assert ts == pytest.approx([0.0, 0.1])


def test_explicit_interval_overrides_fps():
    frames = ordered_records([0, None])
    normalized, _, _ = repair_timestamps(
        frames, explicit_interval=Fraction(1, 25), jump_threshold=2.0
    )
    assert normalized[1].repaired_pts_seconds == pytest.approx(0.04)


def test_invalid_threshold_and_rate_rejected():
    frames = ordered_records([0, 33])
    with pytest.raises(UsageError):
        repair_timestamps(frames, fps=Fraction(30), jump_threshold=0)
    with pytest.raises(UsageError):
        parse_fraction("not-a-rate")
    with pytest.raises(UsageError):
        parse_fraction("1/0")
    with pytest.raises(UsageError):
        frame_interval_seconds(None, None)


def test_original_pts_retained_on_record():
    frames = ordered_records([0, None])
    normalized, _, _ = repair_timestamps(
        frames, fps=Fraction(30), jump_threshold=2.0
    )
    assert normalized[0].original_pts == 0
    assert normalized[1].original_pts is None
