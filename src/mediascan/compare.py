"""Track matching and consistency rules between a file and its baseline.

Matching is by track *type* and ordinal position within that type, so extra,
missing and duplicated tracks are handled without relying on absolute indices.
Every difference carries a stable ``error_code``; output order is deterministic.
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field as dc_field
from typing import Any

from .errors import (
    ERR_BASELINE_MISSING,
    ERR_CODEC_PARAM,
    ERR_DURATION_DRIFT,
    ERR_PARAM_UNKNOWN,
    ERR_TIMEBASE_DRIFT,
    ERR_TRACK_EXTRA,
    ERR_TRACK_MISSING,
    ERR_TRACK_TYPE,
)
from .extract import Record, Track

_FLOAT_EPS = 1e-9


class BaselineError(ValueError):
    """Invalid or unreadable baseline -> CLI exit code 2."""


@dataclass
class ExpectedTrack:
    type: str
    codec_summary: dict[str, Any] = dc_field(default_factory=dict)
    duration_ms: int | None = None
    timescale: int | None = None

    def to_dict(self) -> dict[str, Any]:
        return {
            "type": self.type,
            "codec_summary": dict(self.codec_summary),
            "duration_ms": self.duration_ms,
            "timescale": self.timescale,
        }


@dataclass
class ExpectedFile:
    path: str
    duration_ms: int | None
    tracks: list[ExpectedTrack]


@dataclass
class Baseline:
    files: dict[str, ExpectedFile]

    def get(self, rel_path: str) -> ExpectedFile | None:
        return self.files.get(rel_path)


def load_baseline(path: str) -> Baseline:
    try:
        with open(path, "r", encoding="utf-8") as fh:
            raw = json.load(fh)
    except FileNotFoundError as exc:
        raise BaselineError(f"baseline not found: {path}") from exc
    except PermissionError as exc:
        raise BaselineError(f"baseline not readable: {path}") from exc
    except (OSError, json.JSONDecodeError) as exc:
        raise BaselineError(f"baseline is not valid JSON: {exc}") from exc
    if not isinstance(raw, dict) or not isinstance(raw.get("files"), dict):
        raise BaselineError("baseline must be an object with a 'files' mapping")
    files: dict[str, ExpectedFile] = {}
    for rel, spec in raw["files"].items():
        if not isinstance(rel, str) or not isinstance(spec, dict):
            raise BaselineError("baseline file entries must be objects")
        duration = spec.get("duration_ms")
        if duration is not None and not _is_number(duration):
            raise BaselineError(f"{rel}: duration_ms must be numeric or null")
        tracks_raw = spec.get("tracks", [])
        if not isinstance(tracks_raw, list):
            raise BaselineError(f"{rel}: tracks must be a list")
        tracks: list[ExpectedTrack] = []
        for i, trempl in enumerate(tracks_raw):
            if not isinstance(trempl, dict) or not isinstance(
                trempl.get("type"), str
            ):
                raise BaselineError(f"{rel}: track {i + 1} needs a string 'type'")
            summary = trempl.get("codec_summary")
            if summary is None:
                summary = {
                    k: trempl.get(k)
                    for k in ("codec", "sample_rate", "channels", "bit_depth",
                              "width", "height")
                    if k in trempl
                }
            if not isinstance(summary, dict):
                raise BaselineError(f"{rel}: track {i + 1} codec_summary invalid")
            tdur = trempl.get("duration_ms")
            tscale = trempl.get("timescale")
            if tdur is not None and not _is_number(tdur):
                raise BaselineError(f"{rel}: track {i + 1} duration_ms invalid")
            if tscale is not None and not _is_number(tscale):
                raise BaselineError(f"{rel}: track {i + 1} timescale invalid")
            tracks.append(
                ExpectedTrack(
                    type=trempl["type"],
                    codec_summary=dict(summary),
                    duration_ms=int(tdur) if tdur is not None else None,
                    timescale=int(tscale) if tscale is not None else None,
                )
            )
        files[rel] = ExpectedFile(rel, int(duration) if duration is not None else None,
                                  tracks)
    return Baseline(files)


def _is_number(value: Any) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool)


@dataclass
class Mismatch:
    file: str
    error_code: str
    track_index: int | None
    track_type: str | None
    field: str | None = None
    expected: Any = None
    actual: Any = None
    duration_delta_ms: int | None = None
    duration_delta_relative: float | None = None
    codec_summary: dict[str, Any] = dc_field(default_factory=dict)
    detail: str = ""

    def to_dict(self) -> dict[str, Any]:
        return {
            "file": self.file,
            "error_code": self.error_code,
            "track_index": self.track_index,
            "track_type": self.track_type,
            "field": self.field,
            "expected": self.expected,
            "actual": self.actual,
            "duration_delta_ms": self.duration_delta_ms,
            "duration_delta_relative": self.duration_delta_relative,
            "codec_summary": self.codec_summary,
            "detail": self.detail,
        }

    def sort_key(self) -> tuple:
        return (
            self.file,
            self.track_index if self.track_index is not None else -1,
            self.error_code,
            self.field or "",
        )


@dataclass
class Comparison:
    file: str
    mismatches: list[Mismatch]
    max_duration_delta_ms: int | None
    matched_tracks: int

    @property
    def categories(self) -> dict[str, int]:
        counts = {
            "duration": 0,
            "timebase": 0,
            "param": 0,
            "track_missing": 0,
            "track_extra": 0,
            "track_type": 0,
        }
        for m in self.mismatches:
            if m.error_code == ERR_DURATION_DRIFT:
                counts["duration"] += 1
            elif m.error_code == ERR_TIMEBASE_DRIFT:
                counts["timebase"] += 1
            elif m.error_code in (ERR_CODEC_PARAM, ERR_PARAM_UNKNOWN):
                counts["param"] += 1
            elif m.error_code == ERR_TRACK_MISSING:
                counts["track_missing"] += 1
            elif m.error_code == ERR_TRACK_EXTRA:
                counts["track_extra"] += 1
            elif m.error_code == ERR_TRACK_TYPE:
                counts["track_type"] += 1
        return counts


def compare_record(
    record: Record,
    baseline: Baseline,
    duration_tolerance_ms: int = 0,
    relative_tolerance_pct: float | None = None,
) -> Comparison:
    expected = baseline.get(record.path)
    if expected is None:
        return Comparison(
            record.path,
            [
                Mismatch(
                    file=record.path,
                    error_code=ERR_BASELINE_MISSING,
                    track_index=None,
                    track_type=None,
                    detail="no baseline entry for file",
                )
            ],
            None,
            0,
        )
    mismatches: list[Mismatch] = []
    max_delta: int | None = None
    matched = 0
    for actual, want in _match_tracks(record.tracks, expected.tracks, record.path,
                                      mismatches):
        matched += 1
        pair_mismatches, delta = _compare_track(
            record.path, actual, want, duration_tolerance_ms, relative_tolerance_pct
        )
        mismatches.extend(pair_mismatches)
        if delta is not None:
            max_delta = delta if max_delta is None else max(max_delta, delta)
    mismatches.sort(key=Mismatch.sort_key)
    return Comparison(record.path, mismatches, max_delta, matched)


def _match_tracks(
    actual: list[Track],
    expected: list[ExpectedTrack],
    file: str,
    mismatches: list[Mismatch],
) -> list[tuple[Track, ExpectedTrack]]:
    pairs: list[tuple[Track, ExpectedTrack]] = []
    types = _ordered_types(expected, actual)
    leftover_expected: list[ExpectedTrack] = []
    leftover_actual: list[Track] = []
    for ttype in types:
        a_list = [t for t in actual if t.type == ttype]
        e_list = [t for t in expected if t.type == ttype]
        for a, e in zip(a_list, e_list):
            pairs.append((a, e))
        leftover_actual.extend(a_list[len(e_list):])
        leftover_expected.extend(e_list[len(a_list):])
    leftover_actual.sort(key=lambda t: t.index)
    # Pair leftovers in order: a same-position type change is one finding, not
    # an independent "missing" plus "extra".
    paired = min(len(leftover_expected), len(leftover_actual))
    for e, a in zip(leftover_expected[:paired], leftover_actual[:paired]):
        mismatches.append(
            Mismatch(
                file=file,
                error_code=ERR_TRACK_TYPE,
                track_index=a.index,
                track_type=a.type,
                field="type",
                expected=e.type,
                actual=a.type,
                codec_summary=a.codec_summary(),
                detail=f"track {a.index} changed type {e.type} -> {a.type}",
            )
        )
    for e in leftover_expected[paired:]:
        mismatches.append(
            Mismatch(
                file=file,
                error_code=ERR_TRACK_MISSING,
                track_index=None,
                track_type=e.type,
                field="track",
                expected=e.type,
                actual=None,
                detail=f"baseline {e.type} track is missing",
            )
        )
    for a in leftover_actual[paired:]:
        mismatches.append(
            Mismatch(
                file=file,
                error_code=ERR_TRACK_EXTRA,
                track_index=a.index,
                track_type=a.type,
                field="track",
                expected=None,
                actual=a.type,
                codec_summary=a.codec_summary(),
                detail=f"unexpected {a.type} track at index {a.index}",
            )
        )
    return pairs


def _ordered_types(
    expected: list[ExpectedTrack], actual: list[Track]
) -> list[str]:
    order: list[str] = []
    for t in expected:
        if t.type not in order:
            order.append(t.type)
    for t in actual:
        if t.type not in order:
            order.append(t.type)
    return order


def _compare_track(
    file: str,
    actual: Track,
    want: ExpectedTrack,
    tolerance_ms: int,
    relative_tolerance_pct: float | None,
) -> tuple[list[Mismatch], int | None]:
    mismatches: list[Mismatch] = []
    delta: int | None = None
    relative: float | None = None
    if want.duration_ms is not None or actual.duration_ms is not None:
        if want.duration_ms is None or actual.duration_ms is None:
            mismatches.append(
                Mismatch(
                    file=file,
                    error_code=ERR_PARAM_UNKNOWN,
                    track_index=actual.index,
                    track_type=actual.type,
                    field="duration_ms",
                    expected=want.duration_ms,
                    actual=actual.duration_ms,
                    codec_summary=actual.codec_summary(),
                    detail="duration is unknown on one side",
                )
            )
        else:
            delta = abs(actual.duration_ms - want.duration_ms)
            if want.duration_ms > 0:
                relative = delta * 100.0 / want.duration_ms
            # The relative tolerance, when set, widens the absolute one: a
            # drift must exceed *both* gates to be reported.
            over_abs = delta > tolerance_ms
            over_rel = relative_tolerance_pct is None or (
                relative is not None
                and relative > relative_tolerance_pct + _FLOAT_EPS
            )
            if over_abs and over_rel:
                mismatches.append(
                    Mismatch(
                        file=file,
                        error_code=ERR_DURATION_DRIFT,
                        track_index=actual.index,
                        track_type=actual.type,
                        field="duration_ms",
                        expected=want.duration_ms,
                        actual=actual.duration_ms,
                        duration_delta_ms=delta,
                        duration_delta_relative=(
                            round(relative, 4) if relative is not None else None
                        ),
                        codec_summary=actual.codec_summary(),
                        detail=(
                            f"duration differs by {delta} ms "
                            f"(tolerance {tolerance_ms} ms)"
                        ),
                    )
                )
    if want.timescale is not None:
        if actual.timescale is None:
            mismatches.append(
                Mismatch(
                    file=file,
                    error_code=ERR_PARAM_UNKNOWN,
                    track_index=actual.index,
                    track_type=actual.type,
                    field="timescale",
                    expected=want.timescale,
                    actual=None,
                    duration_delta_ms=delta,
                    codec_summary=actual.codec_summary(),
                    detail="timescale is unknown",
                )
            )
        elif actual.timescale != want.timescale:
            mismatches.append(
                Mismatch(
                    file=file,
                    error_code=ERR_TIMEBASE_DRIFT,
                    track_index=actual.index,
                    track_type=actual.type,
                    field="timescale",
                    expected=want.timescale,
                    actual=actual.timescale,
                    duration_delta_ms=delta,
                    codec_summary=actual.codec_summary(),
                    detail=(
                        f"timescale {actual.timescale} != baseline "
                        f"{want.timescale}"
                    ),
                )
            )
    summary = actual.codec_summary()
    for name in _FIELD_ORDER:
        if name not in want.codec_summary:
            continue
        expected_value = want.codec_summary[name]
        if expected_value is None:
            continue
        actual_value = summary.get(name)
        if actual_value is None:
            mismatches.append(
                Mismatch(
                    file=file,
                    error_code=ERR_PARAM_UNKNOWN,
                    track_index=actual.index,
                    track_type=actual.type,
                    field=name,
                    expected=expected_value,
                    actual=None,
                    duration_delta_ms=delta,
                    codec_summary=summary,
                    detail=f"{name} is unknown",
                )
            )
        elif actual_value != expected_value:
            mismatches.append(
                Mismatch(
                    file=file,
                    error_code=ERR_CODEC_PARAM,
                    track_index=actual.index,
                    track_type=actual.type,
                    field=name,
                    expected=expected_value,
                    actual=actual_value,
                    duration_delta_ms=delta,
                    codec_summary=summary,
                    detail=f"{name} drifted {expected_value} -> {actual_value}",
                )
            )
    return mismatches, delta


_FIELD_ORDER = (
    "codec",
    "sample_rate",
    "channels",
    "bit_depth",
    "width",
    "height",
)
