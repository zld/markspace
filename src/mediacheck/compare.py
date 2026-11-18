"""Track matching and consistency verdicts.

Matching key: ``(kind, language, codec, stable_index)``.  Reordering tracks
never changes the key, duplicate candidates are reported as ``ambiguous``
and never resolved silently, and fields absent on either side are reported
as ``unknown`` rather than being filled with defaults.
"""

from __future__ import annotations

from typing import Any

from .model import SniffResult, Track
from .records import CompareReport, FileReport, Mismatch, _FLOAT_EPSILON


def match_key(track: Track) -> tuple:
    return (track.kind, track.language, track.codec, track.stable_index)


def compare_containers(
    expected: SniffResult | None,
    actual: SniffResult,
    *,
    path: str = "",
    duration_tolerance_ms: float = 0.0,
) -> FileReport:
    report = FileReport(path=path, expected=expected, actual=actual)
    if actual.error:
        report.mismatches.append(
            Mismatch(
                code="file_error",
                path=path,
                detail=actual.error,
                actual=actual.error,
            )
        )
        return report
    if expected is None:
        return report
    if expected.error:
        report.mismatches.append(
            Mismatch(
                code="expected_error",
                path=path,
                detail=f"expected snapshot is unusable: {expected.error}",
                expected=expected.error,
            )
        )
        return report
    if expected.container != actual.container:
        report.mismatches.append(
            Mismatch(
                code="container_changed",
                path=path,
                detail=(
                    f"container changed from {expected.container} "
                    f"to {actual.container}"
                ),
                expected=expected.container,
                actual=actual.container,
            )
        )
    if actual.truncated:
        report.mismatches.append(
            Mismatch(
                code="truncated",
                path=path,
                detail="container ends before its declared size",
                expected="complete",
                actual="truncated",
            )
        )
    if len(expected.tracks) != len(actual.tracks):
        report.mismatches.append(
            Mismatch(
                code="track_count",
                path=path,
                detail=(
                    f"expected {len(expected.tracks)} track(s), "
                    f"found {len(actual.tracks)}"
                ),
                expected=len(expected.tracks),
                actual=len(actual.tracks),
            )
        )

    expected_groups = _group(expected.tracks)
    actual_groups = _group(actual.tracks)
    for key in sorted(
        set(expected_groups) | set(actual_groups), key=_sort_key
    ):
        exp = expected_groups.get(key, [])
        act = actual_groups.get(key, [])
        if len(exp) > 1 or len(act) > 1:
            report.mismatches.append(
                Mismatch(
                    code="ambiguous",
                    path=path,
                    detail=(
                        f"identity {_describe_key(key)} has {len(exp)} "
                        f"expected and {len(act)} actual candidate(s); "
                        "refusing to pick one"
                    ),
                    expected=len(exp),
                    actual=len(act),
                    tracks=[t.stable_index for t in exp + act],
                )
            )
            continue
        if not exp:
            report.mismatches.append(
                Mismatch(
                    code="track_missing",
                    path=path,
                    detail=f"unexpected extra track {_describe_key(key)}",
                    expected=None,
                    actual=_summarise(act[0]),
                    tracks=[act[0].stable_index],
                )
            )
            continue
        if not act:
            report.mismatches.append(
                Mismatch(
                    code="track_missing",
                    path=path,
                    detail=f"expected track {_describe_key(key)} is missing",
                    expected=_summarise(exp[0]),
                    actual=None,
                    tracks=[exp[0].stable_index],
                )
            )
            continue
        _compare_track(report, path, exp[0], act[0], duration_tolerance_ms)

    _compare_declared_duration(report, path, expected, actual, duration_tolerance_ms)
    return report


def _compare_track(
    report: FileReport,
    path: str,
    expected: Track,
    actual: Track,
    tolerance: float,
) -> None:
    if expected.duration_ms is not None and actual.duration_ms is not None:
        deviation = round(actual.duration_ms - expected.duration_ms, 3)
        report.duration_deviations_ms.append(deviation)
        if abs(deviation) > tolerance + _FLOAT_EPSILON:
            report.mismatches.append(
                Mismatch(
                    code="duration_deviation",
                    path=path,
                    detail=(
                        f"track {_describe(actual)} deviates {deviation:+.3f} ms "
                        f"(tolerance {tolerance:g} ms)"
                    ),
                    expected=expected.duration_ms,
                    actual=deviation,
                    tracks=[actual.stable_index],
                )
            )
    elif (expected.duration_ms is None) != (actual.duration_ms is None):
        report.mismatches.append(
            Mismatch(
                code="unknown_duration",
                path=path,
                detail=f"track {_describe(actual)} has an unknown duration on one side",
                expected=expected.duration_ms,
                actual=actual.duration_ms,
                tracks=[actual.stable_index],
            )
        )

    for name in ("sample_rate", "channels", "bit_depth", "block_align"):
        exp_value = getattr(expected, name)
        act_value = getattr(actual, name)
        if exp_value is None or act_value is None:
            if exp_value is not None or act_value is not None:
                report.mismatches.append(
                    Mismatch(
                        code="unknown_field",
                        path=path,
                        detail=(
                            f"track {_describe(actual)} field {name} is "
                            "unknown on one side"
                        ),
                        expected=exp_value,
                        actual=act_value,
                        tracks=[actual.stable_index],
                    )
                )
            continue
        if exp_value != act_value:
            report.mismatches.append(
                Mismatch(
                    code="parameter_drift",
                    path=path,
                    detail=(
                        f"track {_describe(actual)} {name} changed from "
                        f"{exp_value} to {act_value}"
                    ),
                    expected=exp_value,
                    actual=act_value,
                    tracks=[actual.stable_index],
                )
            )

    exp_digest = expected.extra.get("codec_digest")
    act_digest = actual.extra.get("codec_digest")
    if exp_digest and act_digest and exp_digest != act_digest:
        report.mismatches.append(
            Mismatch(
                code="codec_digest",
                path=path,
                detail=(
                    f"track {_describe(actual)} sample entry digest changed "
                    f"from {exp_digest} to {act_digest}"
                ),
                expected=exp_digest,
                actual=act_digest,
                tracks=[actual.stable_index],
            )
        )


def _compare_declared_duration(
    report: FileReport,
    path: str,
    expected: SniffResult,
    actual: SniffResult,
    tolerance: float,
) -> None:
    exp = expected.declared_duration_ms
    act = actual.declared_duration_ms
    if exp is None and act is None:
        return
    if exp is not None and act is not None:
        deviation = round(act - exp, 3)
        if abs(deviation) > tolerance + _FLOAT_EPSILON:
            report.mismatches.append(
                Mismatch(
                    code="declared_duration_deviation",
                    path=path,
                    detail=(
                        f"container duration deviates {deviation:+.3f} ms "
                        f"(tolerance {tolerance:g} ms)"
                    ),
                    expected=exp,
                    actual=deviation,
                )
            )
        return
    report.mismatches.append(
        Mismatch(
            code="unknown_duration",
            path=path,
            detail="container duration is unknown on one side",
            expected=exp,
            actual=act,
        )
    )


def _group(tracks: list[Track]) -> dict[tuple, list[Track]]:
    grouped: dict[tuple, list[Track]] = {}
    for track in tracks:
        grouped.setdefault(match_key(track), []).append(track)
    return grouped


def _sort_key(key: tuple) -> tuple[str, str, str, int]:
    """Deterministic ordering for identity keys, tolerating ``None``."""
    kind, language, codec, index = key
    return (
        str(kind),
        language or "",
        codec or "",
        int(index or 0),
    )


def _describe(track: Track) -> str:
    return f"#{track.stable_index} ({track.kind}/{track.codec or '?'})"


def _describe_key(key: tuple) -> str:
    kind, language, codec, index = key
    return f"{kind}/{language or '?'}/{codec or '?'}/#{index}"


def _summarise(track: Track) -> dict[str, Any]:
    return {
        "stable_index": track.stable_index,
        "kind": track.kind,
        "language": track.language,
        "codec": track.codec,
        "duration_ms": track.duration_ms,
    }
