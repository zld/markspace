"""Cross-version consistency checks on unified track models.

Pairing is by track kind and ordinal.  When ordinal pairing would hide a
plausible alternative match (same codec at a different ordinal), the pairing
is reported as ambiguous with the candidate track numbers listed, instead of
silently picking one.
"""

from __future__ import annotations

from typing import Any, Iterable

from .codecs import normalize_codec
from .model import (
    KIND_AUDIO,
    KIND_OTHER,
    KIND_SUBTITLE,
    KIND_VIDEO,
    MediaFile,
    Mismatch,
    Track,
    ValidationResult,
)
from .pairing import check_ordinal_types, ignored, pair_group

KIND_ORDER = (KIND_VIDEO, KIND_AUDIO, KIND_SUBTITLE, KIND_OTHER)

# A duration difference smaller than this is treated as rounding noise and
# is not reported at all.
DURATION_EPSILON_MS = 0.5

COMPARABLE_FIELDS = (
    "kind", "codec", "time_base", "duration_ms", "language",
    "width", "height", "channels", "sample_rate",
)


def _field_value(track: Track | None, name: str) -> Any:
    if track is None:
        return None
    if name == "codec":
        return normalize_codec(track.codec)
    if name == "kind":
        return track.kind
    if name == "time_base":
        return track.time_base
    if name == "duration_ms":
        return track.duration_ms
    if name == "language":
        return track.language
    return track.codec_detail.get(name)


def compare_media(baseline: MediaFile, candidate: MediaFile,
                  tolerance_ms: float = 40.0,
                  ignore_fields: Iterable[str] = ()) -> ValidationResult:
    ignore_set = {f.strip() for f in ignore_fields if f.strip()}
    result = ValidationResult(
        baseline=baseline,
        candidate=candidate,
        tolerance_ms=tolerance_ms,
        ignored_fields=sorted(ignore_set),
    )
    if baseline.fatal or candidate.fatal:
        return result

    _compare_file_duration(result, ignore_set)
    _compare_tracks(result, ignore_set)
    return result


def _compare_file_duration(result: ValidationResult,
                          ignore_fields: set[str]) -> None:
    if ignored("duration_ms", ignore_fields):
        return
    delta = result.duration_delta_ms
    if delta is None or abs(delta) < DURATION_EPSILON_MS:
        return
    blocking = abs(delta) > result.tolerance_ms
    result.mismatches.append(Mismatch(
        kind="duration_out_of_tolerance" if blocking else "duration_drift",
        message=(
            f"file duration differs by {delta:+.3f} ms "
            f"({'outside' if blocking else 'within'} "
            f"{result.tolerance_ms:g} ms tolerance)"
        ),
        field="duration_ms",
        baseline_value=result.baseline.duration_ms,
        candidate_value=result.candidate.duration_ms,
        offset=_first_offset(result.candidate),
    ))


def _first_offset(media: MediaFile) -> int | None:
    for issue in media.issues:
        if issue.offset is not None:
            return issue.offset
    return 0 if media.tracks else None


def _compare_tracks(result: ValidationResult,
                    ignore_fields: set[str]) -> None:
    base = sorted(result.baseline.tracks, key=lambda t: t.track_id)
    cand = sorted(result.candidate.tracks, key=lambda t: t.track_id)

    used_base: set[int] = set()
    used_cand: set[int] = set()
    check_ordinal_types(result, base, cand)

    for kind in KIND_ORDER:
        base_group = [i for i, t in enumerate(base)
                      if t.kind == kind and i not in used_base]
        cand_group = [i for i, t in enumerate(cand)
                      if t.kind == kind and i not in used_cand]
        pairs = pair_group(result, base, cand, base_group, cand_group)
        for bi, ci in pairs:
            used_base.add(bi)
            used_cand.add(ci)
            _compare_pair(result, base[bi], cand[ci], ignore_fields)

    for i, track in enumerate(base):
        if i in used_base:
            continue
        result.mismatches.append(Mismatch(
            kind="track_missing",
            message=f"baseline {track.kind} track {track.track_id} has no "
                    f"candidate counterpart",
            baseline_track_id=track.track_id,
            field="track",
            baseline_value={"kind": track.kind, "codec": track.codec},
            offset=track.offset,
        ))
    for i, track in enumerate(cand):
        if i in used_cand:
            continue
        result.mismatches.append(Mismatch(
            kind="track_extra",
            message=f"candidate {track.kind} track {track.track_id} has no "
                    f"baseline counterpart",
            candidate_track_id=track.track_id,
            field="track",
            candidate_value={"kind": track.kind, "codec": track.codec},
            offset=track.offset,
        ))


def _compare_pair(result: ValidationResult, base: Track, cand: Track,
                  ignore_fields: set[str]) -> None:
    for name in COMPARABLE_FIELDS:
        if ignored(name, ignore_fields):
            continue
        if name == "duration_ms":
            _compare_track_duration(result, base, cand)
            continue
        left, right = _field_value(base, name), _field_value(cand, name)
        if left is None and right is None:
            continue
        if left == right:
            continue
        result.mismatches.append(Mismatch(
            kind=_kind_for_field(name),
            message=(f"{base.kind} track {base.track_id} field {name} "
                     f"changed: {left!r} -> {right!r}"),
            baseline_track_id=base.track_id,
            candidate_track_id=cand.track_id,
            field=name,
            baseline_value=left,
            candidate_value=right,
            offset=cand.offset,
        ))


def _kind_for_field(name: str) -> str:
    if name == "codec":
        return "codec_change"
    if name == "time_base":
        return "time_base_change"
    if name == "language":
        return "language_change"
    return "codec_parameter_change"


def _compare_track_duration(result: ValidationResult, base: Track,
                            cand: Track) -> None:
    if base.duration_ms is None or cand.duration_ms is None:
        if base.duration_ms != cand.duration_ms:
            result.mismatches.append(Mismatch(
                kind="track_duration_unknown",
                message=(f"{base.kind} track {base.track_id} duration is "
                         f"{base.duration_ms!r} in the baseline and "
                         f"{cand.duration_ms!r} in the candidate"),
                baseline_track_id=base.track_id,
                candidate_track_id=cand.track_id,
                field="duration_ms",
                baseline_value=base.duration_ms,
                candidate_value=cand.duration_ms,
                offset=cand.offset,
            ))
        return
    delta = cand.duration_ms - base.duration_ms
    if abs(delta) < DURATION_EPSILON_MS:
        return
    blocking = abs(delta) > result.tolerance_ms
    result.mismatches.append(Mismatch(
        kind="track_duration_out_of_tolerance" if blocking
        else "track_duration_drift",
        message=(f"{base.kind} track {base.track_id} duration differs by "
                 f"{delta:+.3f} ms ({'outside' if blocking else 'within'} "
                 f"{result.tolerance_ms:g} ms tolerance)"),
        baseline_track_id=base.track_id,
        candidate_track_id=cand.track_id,
        field="duration_ms",
        baseline_value=base.duration_ms,
        candidate_value=cand.duration_ms,
        offset=cand.offset,
    ))


def blocking_mismatches(result: ValidationResult) -> list[Mismatch]:
    """Mismatches that should stop a build: everything except soft drift."""
    soft_kinds = {
        "duration_drift", "track_duration_drift", "track_duration_unknown",
        "time_base_change",
    }
    return [m for m in result.mismatches if m.kind not in soft_kinds]
