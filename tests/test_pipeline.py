"""End-to-end and error-path tests for the cutline pipeline."""

from __future__ import annotations

import json
from fractions import Fraction
from pathlib import Path

import pytest

from cutline.rational import RationalTime, TimeError, parse_rate, round_half_away


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
