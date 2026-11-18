"""Shared validation helpers for manifest fields."""

from __future__ import annotations

import math

from .errors import ManifestError


def finite_number(value, field_name: str, *, track_id: str | None = None) -> float:
    """Return ``value`` as a finite float or report the offending field."""
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ManifestError("must be a number", field=field_name, track_id=track_id)
    number = float(value)
    if not math.isfinite(number):
        raise ManifestError("must be finite", field=field_name, track_id=track_id)
    return number


def positive_int(value, field_name: str, *, track_id: str | None = None) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise ManifestError("must be an integer", field=field_name, track_id=track_id)
    if value <= 0:
        raise ManifestError("must be > 0", field=field_name, track_id=track_id)
    return value


def check_id(value: str, field_name: str) -> None:
    if not isinstance(value, str) or not value.strip():
        raise ManifestError("id must be a non-empty string", field=field_name)
    if "/" in value or "\\" in value:
        raise ManifestError("id must not contain path separators", field=field_name)
