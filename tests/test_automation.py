"""Automation curves: domain rules, endpoint hold and reviewable samples."""

from __future__ import annotations

import numpy as np
import pytest

from mixforge.automation import parse_automation
from mixforge.errors import ManifestError


def _curve(points):
    return parse_automation(points, "test")


def test_linear_and_db_points_mix() -> None:
    curve = _curve([
        {"ms": 0, "value": -6.0, "unit": "db"},
        {"ms": 1000, "value": 0.0, "unit": "db"},
        {"ms": 2000, "value": 0.5},
    ])
    assert curve.gain_at_ms(0) == pytest.approx(10 ** (-6 / 20))
    assert curve.gain_at_ms(1000) == pytest.approx(1.0, abs=1e-6)
    assert curve.gain_at_ms(2000) == pytest.approx(0.5)


def test_interpolation_is_linear_between_nodes() -> None:
    curve = _curve([{"ms": 0, "value": 0.0}, {"ms": 1000, "value": 1.0}])
    assert curve.gain_at_ms(250) == pytest.approx(0.25)
    assert curve.gain_at_ms(500) == pytest.approx(0.5)
    assert curve.gain_at_ms(750) == pytest.approx(0.75)


def test_endpoint_hold_outside_curve() -> None:
    curve = _curve([{"ms": 100, "value": -3.0, "unit": "db"},
                    {"ms": 200, "value": -6.0, "unit": "db"}])
    assert curve.gain_at_ms(0) == pytest.approx(10 ** (-3 / 20))
    assert curve.gain_at_ms(10_000) == pytest.approx(10 ** (-6 / 20))


def test_sampled_output_is_reviewable() -> None:
    curve = _curve([{"ms": 0, "value": 0.0}, {"ms": 100, "value": 1.0}])
    sampled = curve.sampled(np.array([0.0, 50.0, 100.0, 150.0]))
    rows = sampled.as_list()
    assert [row["ms"] for row in rows] == [0.0, 50.0, 100.0, 150.0]
    assert [round(row["gain"], 6) for row in rows] == [0.0, 0.5, 1.0, 1.0]


def test_duplicate_time_rejected() -> None:
    with pytest.raises(ManifestError, match="duplicate") as excinfo:
        _curve([{"ms": 100, "value": 0}, {"ms": 100, "value": 1}])
    assert excinfo.value.field.endswith("[1].ms")


def test_decreasing_time_rejected() -> None:
    with pytest.raises(ManifestError, match="increasing"):
        _curve([{"ms": 100, "value": 0}, {"ms": 50, "value": 1}])


def test_single_point_rejected() -> None:
    with pytest.raises(ManifestError, match="at least 2"):
        _curve([{"ms": 0, "value": 0}])


@pytest.mark.parametrize("point,field", [
    ({"value": 0}, ".ms"),
    ({"ms": 0}, ".value"),
    ({"ms": 0, "value": 0, "unit": "percent"}, ".unit"),
])
def test_bad_points_are_located(point, field) -> None:
    with pytest.raises(ManifestError) as excinfo:
        _curve([point, {"ms": 100, "value": 1}])
    assert field in str(excinfo.value.field)


def test_missing_automation_is_none() -> None:
    assert parse_automation(None, "test") is None
