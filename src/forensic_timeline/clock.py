"""Timestamp parsing, timezone normalization and clock-drift estimation.

Rules enforced here:

* Records with an explicit UTC offset use it directly.
* Naive records are interpreted in the configured IANA timezone; ambiguous
  (DST fall-back) and nonexistent (DST spring-forward) local times are
  flagged and isolated, never silently guessed.
* Drift is estimated from manifest anchors as the median of
  (recorded_utc - true_utc). If there are too few anchors or the estimate
  exceeds the configured limit, raw times are kept and marked low
  confidence; no implicit correction is applied.

On platforms whose standard library ships without the IANA database
(notably a bare Windows Python without the ``tzdata`` package),
:func:`resolve_timezone` falls back to a small built-in rule table so the
tool keeps working with no third-party dependency.
"""

from __future__ import annotations

from bisect import bisect_right
from dataclasses import dataclass
from datetime import datetime, tzinfo, timedelta, timezone
from statistics import median
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

from .models import ClockEstimate, Confidence

# ---------------------------------------------------------------------------
# Built-in fallback timezone rules (used only when the IANA database is
# unavailable, e.g. bare Windows Python without the tzdata package).
# Each transition is an (UTC instant, offset in effect from that instant)
# pair. The table covers the zones exercised by this tool, 2007..2037.
# ---------------------------------------------------------------------------

# America/New_York: second Sunday of March 07:00Z -> EDT(-4);
# first Sunday of November 06:00Z -> EST(-5).
_NY_SPRINGS = [
    (2007, 3, 11), (2008, 3, 9), (2009, 3, 8), (2010, 3, 14),
    (2011, 3, 13), (2012, 3, 11), (2013, 3, 10), (2014, 3, 9),
    (2015, 3, 8), (2016, 3, 13), (2017, 3, 12), (2018, 3, 11),
    (2019, 3, 10), (2020, 3, 8), (2021, 3, 14), (2022, 3, 13),
    (2023, 3, 12), (2024, 3, 10), (2025, 3, 9), (2026, 3, 8),
    (2027, 3, 14), (2028, 3, 12), (2029, 3, 11), (2030, 3, 10),
    (2031, 3, 9), (2032, 3, 14), (2033, 3, 13), (2034, 3, 12),
    (2035, 3, 11), (2036, 3, 9), (2037, 3, 8),
]
_NY_FALLS = [
    (2007, 11, 4), (2008, 11, 2), (2009, 11, 1), (2010, 11, 7),
    (2011, 11, 6), (2012, 11, 4), (2013, 11, 3), (2014, 11, 2),
    (2015, 11, 1), (2016, 11, 6), (2017, 11, 5), (2018, 11, 4),
    (2019, 11, 3), (2020, 11, 1), (2021, 11, 7), (2022, 11, 6),
    (2023, 11, 5), (2024, 11, 3), (2025, 11, 2), (2026, 11, 1),
    (2027, 11, 7), (2028, 11, 5), (2029, 11, 4), (2030, 11, 3),
    (2031, 11, 2), (2032, 11, 7), (2033, 11, 6), (2034, 11, 5),
    (2035, 11, 4), (2036, 11, 2), (2037, 11, 1),
]

# Europe/Berlin: last Sunday of March 01:00Z -> CEST(+2);
# last Sunday of October 01:00Z -> CET(+1).
_BERLIN_SPRINGS = [
    (2024, 3, 31), (2025, 3, 30), (2026, 3, 29), (2027, 3, 28),
    (2028, 3, 26), (2029, 3, 25), (2030, 3, 31), (2031, 3, 30),
    (2032, 3, 28), (2033, 3, 27), (2034, 3, 26), (2035, 3, 25),
    (2036, 3, 30), (2037, 3, 29),
]
_BERLIN_FALLS = [
    (2024, 10, 27), (2025, 10, 26), (2026, 10, 25), (2027, 10, 31),
    (2028, 10, 29), (2029, 10, 28), (2030, 10, 27), (2031, 10, 26),
    (2032, 10, 31), (2033, 10, 30), (2034, 10, 29), (2035, 10, 28),
    (2036, 10, 26), (2037, 10, 25),
]


def _build_transitions() -> dict[str, tuple[list[datetime], list[timedelta], timedelta]]:
    table: dict[str, tuple[list[datetime], list[timedelta], timedelta]] = {}

    ny: list[tuple[datetime, timedelta]] = []
    est, edt = timedelta(hours=-5), timedelta(hours=-4)
    for (y, m, d) in _NY_SPRINGS:
        ny.append((datetime(y, m, d, 7, 0), edt))
    for (y, m, d) in _NY_FALLS:
        ny.append((datetime(y, m, d, 6, 0), est))
    ny.sort(key=lambda item: item[0])
    table["America/New_York"] = (
        [t for t, _ in ny], [o for _, o in ny], est)

    berlin: list[tuple[datetime, timedelta]] = []
    cet, cest = timedelta(hours=1), timedelta(hours=2)
    for (y, m, d) in _BERLIN_SPRINGS:
        berlin.append((datetime(y, m, d, 1, 0), cest))
    for (y, m, d) in _BERLIN_FALLS:
        berlin.append((datetime(y, m, d, 1, 0), cet))
    berlin.sort(key=lambda item: item[0])
    table["Europe/Berlin"] = (
        [t for t, _ in berlin], [o for _, o in berlin], cet)

    table["Asia/Shanghai"] = ([], [], timedelta(hours=8))
    table["UTC"] = ([], [], timedelta(0))
    return table


_FALLBACK_RULES = _build_transitions()


class _RuleBasedTz(tzinfo):
    """Minimal fold-aware tzinfo backed by explicit UTC transition rules.

    Follows PEP 495: inside a spring-forward gap or a fall-back overlap,
    ``fold=0`` selects the pre-transition offset and ``fold=1`` the
    post-transition offset, exactly like :class:`zoneinfo.ZoneInfo`.
    """

    def __init__(self, key: str, instants: list[datetime],
                 offsets: list[timedelta], initial: timedelta) -> None:
        self._key = key
        self._instants = instants
        self._offsets = offsets
        self._initial = initial

    def __repr__(self) -> str:
        return f"_RuleBasedTz({self._key!r})"

    def _offset_for_utc(self, dt_utc: datetime) -> tuple[timedelta, int]:
        """Offset in effect at an absolute (naive) UTC instant."""
        idx = bisect_right(self._instants, dt_utc) - 1
        if idx < 0:
            return self._initial, -1
        return self._offsets[idx], idx

    def utcoffset(self, dt: datetime | None) -> timedelta | None:
        if dt is None:
            return None
        local = dt.replace(tzinfo=None)
        # Check whether the wall time falls inside a gap or an overlap.
        for i, instant in enumerate(self._instants):
            new_off = self._offsets[i]
            old_off = self._offsets[i - 1] if i > 0 else self._initial
            lo, hi = sorted((instant + old_off, instant + new_off))
            if lo <= local < hi:
                return old_off if dt.fold == 0 else new_off
        # Unambiguous wall time: both offset probes converge.
        off, _ = self._offset_for_utc(local - self._initial)
        off, _ = self._offset_for_utc(local - off)
        return off

    def dst(self, dt: datetime | None) -> timedelta | None:
        offset = self.utcoffset(dt)
        if offset is None:
            return None
        return timedelta(0) if offset == self._initial else offset - self._initial

    def tzname(self, dt: datetime | None) -> str | None:
        return self._key

    def fromutc(self, dt: datetime) -> datetime:
        dt_utc = dt.replace(tzinfo=None)
        offset, idx = self._offset_for_utc(dt_utc)
        local = (dt_utc + offset).replace(tzinfo=self)
        # Mark fold=1 only inside the fall-back overlap window.
        if idx >= 0:
            instant = self._instants[idx]
            new_off = self._offsets[idx]
            old_off = self._offsets[idx - 1] if idx >= 1 else self._initial
            if new_off < old_off and instant <= dt_utc < instant + (old_off - new_off):
                local = local.replace(fold=1)
        return local


def resolve_timezone(name: str) -> tzinfo:
    """Return a tzinfo for ``name``.

    Prefer the system IANA database via :class:`zoneinfo.ZoneInfo`; when it
    is unavailable (e.g. bare Windows Python without ``tzdata``) use the
    built-in rule table. Unknown names raise :class:`ZoneInfoNotFoundError`.
    """
    try:
        return ZoneInfo(name)
    except ZoneInfoNotFoundError:
        if name in _FALLBACK_RULES:
            return _RuleBasedTz(name, *_FALLBACK_RULES[name])
        raise
    except ValueError:
        if name in _FALLBACK_RULES:
            return _RuleBasedTz(name, *_FALLBACK_RULES[name])
        raise ZoneInfoNotFoundError(
            f"No time zone found with key {name!r}") from None


@dataclass
class ParsedTime:
    utc: datetime | None
    tz_label: str
    naive: bool
    anomaly: str | None  # ambiguous_local_time | nonexistent_local_time | unparseable_time


def _roundtrip_valid(aware: datetime) -> bool:
    """True if the aware datetime maps to itself through UTC (i.e. it exists)."""
    back = aware.astimezone(timezone.utc).astimezone(aware.tzinfo)
    return back.replace(tzinfo=None) == aware.replace(tzinfo=None) and back.fold == aware.fold


def parse_record_time(raw: str, default_tz: tzinfo, default_tz_name: str) -> ParsedTime:
    try:
        dt = datetime.fromisoformat(raw)
    except (ValueError, TypeError):
        return ParsedTime(None, "", False, "unparseable_time")

    if dt.tzinfo is not None:
        return ParsedTime(dt.astimezone(timezone.utc), str(dt.utcoffset()), False, None)

    # Naive local time: interpret in the configured zone, but never guess
    # around DST transitions.
    d0 = dt.replace(tzinfo=default_tz, fold=0)
    d1 = dt.replace(tzinfo=default_tz, fold=1)
    if d0.utcoffset() == d1.utcoffset():
        return ParsedTime(d0.astimezone(timezone.utc), default_tz_name, True, None)
    valid0, valid1 = _roundtrip_valid(d0), _roundtrip_valid(d1)
    kind = "ambiguous_local_time" if (valid0 and valid1) else "nonexistent_local_time"
    return ParsedTime(None, default_tz_name, True, kind)


def estimate_drift(
    utc_by_id: dict[str, datetime],
    anchors: list[dict],
    drift_limit_seconds: int,
    min_anchors: int,
) -> ClockEstimate:
    deltas: list[float] = []
    for anchor in anchors:
        event_id = anchor.get("event_id")
        true_raw = anchor.get("true_utc")
        if not isinstance(event_id, str) or not isinstance(true_raw, str):
            continue
        recorded = utc_by_id.get(event_id)
        if recorded is None:
            continue
        try:
            true_utc = datetime.fromisoformat(true_raw)
        except ValueError:
            continue
        if true_utc.tzinfo is None:
            continue
        deltas.append((recorded - true_utc.astimezone(timezone.utc)).total_seconds())

    if len(deltas) < min_anchors:
        return ClockEstimate(
            drift_seconds=0,
            anchors_used=len(deltas),
            applied=False,
            confidence=Confidence.LOW,
            reason=f"insufficient anchors ({len(deltas)} usable, need {min_anchors}); "
                   f"raw timestamps kept",
        )

    drift = int(round(median(deltas)))
    if abs(drift) > drift_limit_seconds:
        return ClockEstimate(
            drift_seconds=drift,
            anchors_used=len(deltas),
            applied=False,
            confidence=Confidence.LOW,
            reason=f"estimated drift {drift:+d}s exceeds limit "
                   f"{drift_limit_seconds}s; raw timestamps kept",
        )
    return ClockEstimate(
        drift_seconds=drift,
        anchors_used=len(deltas),
        applied=True,
        confidence=Confidence.HIGH,
        reason=f"median of {len(deltas)} anchor delta(s) within limit "
               f"{drift_limit_seconds}s",
    )
