"""Compare a baseline container against an input container track by track."""
from __future__ import annotations

from .model import ComparisonResult, Media, Mismatch, Track, TrackError

# Deterministic ordering of mismatch kinds in reports.
_KIND_RANK = {
    "missing_track": 0,
    "extra_track": 1,
    "order_changed": 2,
    "renamed": 3,
    "duration_drift": 4,
    "parameter_drift": 5,
}


def _sorted_mismatches(items: list[Mismatch]) -> list[Mismatch]:
    return sorted(
        items,
        key=lambda m: (_KIND_RANK.get(m.kind, 99), m.track_id is None,
                       m.track_id if m.track_id is not None else -1,
                       m.field or ""),
    )


def _param_fields(track: Track) -> dict[str, int | str]:
    """Engine-relevant encoding parameters for one track."""
    fields: dict[str, int | str] = {
        "kind": track.kind_name,
        "codec_id": track.codec_id,
        "timebase": track.timebase,
    }
    if track.kind_name == "video":
        fields["width"] = track.width
        fields["height"] = track.height
    if track.kind_name == "audio":
        fields["sample_rate"] = track.sample_rate
        fields["channels"] = track.channels
    return fields


def _compare_track(base: Track, other: Track, base_index: int,
                   other_index: int, tolerance_ms: float,
                   baseline_path: str, input_path: str) -> list[Mismatch]:
    out: list[Mismatch] = []
    common = dict(baseline_path=baseline_path, input_path=input_path)
    if base_index != other_index:
        out.append(Mismatch(
            kind="order_changed", track_id=base.track_id,
            message=f"track {base.track_id} moved from position "
                    f"{base_index} to {other_index}",
            baseline=base_index, input_value=other_index, **common,
        ))
    if base.language != other.language:
        out.append(Mismatch(
            kind="renamed", track_id=base.track_id,
            message=f"track {base.track_id} language label changed: "
                    f"{base.language!r} -> {other.language!r}",
            field="language", baseline=base.language,
            input_value=other.language, **common,
        ))
    delta = other.duration_ms - base.duration_ms
    if abs(delta) > tolerance_ms:
        out.append(Mismatch(
            kind="duration_drift", track_id=base.track_id,
            message=f"track {base.track_id} duration drift {delta:+.3f}ms "
                    f"exceeds tolerance {tolerance_ms:g}ms",
            field="duration_ms", baseline=round(base.duration_ms, 3),
            input_value=round(other.duration_ms, 3), delta_ms=delta,
            **common,
        ))
    base_params = _param_fields(base)
    other_params = _param_fields(other)
    for key in sorted(set(base_params) | set(other_params)):
        if base_params.get(key) != other_params.get(key):
            out.append(Mismatch(
                kind="parameter_drift", track_id=base.track_id,
                message=f"track {base.track_id} parameter {key} changed: "
                        f"{base_params.get(key)!r} -> {other_params.get(key)!r}",
                field=key, baseline=base_params.get(key),
                input_value=other_params.get(key), **common,
            ))
    return out


def compare_media(baseline: Media, candidate: Media,
                  tolerance_ms: float) -> ComparisonResult:
    """Compare by track id and return a fully populated result record."""
    mismatches: list[Mismatch] = []
    base_ids = {t.track_id for t in baseline.tracks}
    input_ids = {t.track_id for t in candidate.tracks}
    base_index = {t.track_id: i for i, t in enumerate(baseline.tracks)}
    input_index = {t.track_id: i for i, t in enumerate(candidate.tracks)}

    for missing_id in sorted(base_ids - input_ids):
        mismatches.append(Mismatch(
            kind="missing_track", track_id=missing_id,
            message=f"track {missing_id} present in baseline but missing "
                    f"from input",
            baseline_path=baseline.path, input_path=candidate.path,
        ))
    for extra_id in sorted(input_ids - base_ids):
        mismatches.append(Mismatch(
            kind="extra_track", track_id=extra_id,
            message=f"track {extra_id} present in input but absent from "
                    f"baseline",
            baseline_path=baseline.path, input_path=candidate.path,
        ))
    for shared_id in sorted(base_ids & input_ids):
        base_track = baseline.track_by_id(shared_id)
        other_track = candidate.track_by_id(shared_id)
        assert base_track is not None and other_track is not None
        mismatches.extend(_compare_track(
            base_track, other_track, base_index[shared_id],
            input_index[shared_id], tolerance_ms,
            baseline.path, candidate.path,
        ))

    total_delta = candidate.duration_ms - baseline.duration_ms
    if abs(total_delta) > tolerance_ms:
        mismatches.append(Mismatch(
            kind="duration_drift", track_id=None,
            message=f"total duration drift {total_delta:+.3f}ms exceeds "
                    f"tolerance {tolerance_ms:g}ms",
            field="duration_ms", baseline=round(baseline.duration_ms, 3),
            input_value=round(candidate.duration_ms, 3),
            delta_ms=total_delta,
            baseline_path=baseline.path, input_path=candidate.path,
        ))

    errors: list[TrackError] = [*baseline.errors, *candidate.errors]
    return ComparisonResult(
        baseline_path=baseline.path,
        input_path=candidate.path,
        tolerance_ms=float(tolerance_ms),
        baseline_tracks=len(baseline.tracks),
        input_tracks=len(candidate.tracks),
        baseline_duration_ms=baseline.duration_ms,
        input_duration_ms=candidate.duration_ms,
        duration_delta_ms=total_delta,
        mismatches=_sorted_mismatches(mismatches),
        errors=errors,
    )
