"""Cross-version consistency rules.

A *group* is the directory that holds a batch of versions of the same asset.
Inside a group the lexicographically first file (by relative path) is the
reference; every other file is compared against it.  Rules never invent
values: metadata that could not be extracted is reported as ``unknown``.
"""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from typing import Any

from .models import UNKNOWN, Container, FileResult, Issue, Track, issue_sort_key

RULES_VERSION = "1.0"
PROBLEM_RULES = {
    "duplicate-track-number": ("R007", "duplicate-track-number", "error"),
    "track-duration-overflow": ("R008", "track-duration-overflow", "warning"),
    "unknown track type": ("R009", "unknown-metadata", "warning"),
    "track duration present but time base unknown": (
        "R009", "unknown-metadata", "warning",
    ),
    "stsd declares no sample entries": ("R009", "unknown-metadata", "warning"),
}


@dataclass
class BatchReport:
    files: list[FileResult] = field(default_factory=list)
    issues: list[Issue] = field(default_factory=list)
    duration_deviation_ms: int = 0
    codec_summary: dict[str, list[str]] = field(default_factory=dict)

    @property
    def track_count(self) -> int:
        return sum(len(f.container.tracks) for f in self.files)

    def scan_dict(self, batch_id: str) -> dict[str, Any]:
        return {
            "batch_id": batch_id,
            "files": len(self.files),
            "tracks": self.track_count,
            "duration_deviation_ms": self.duration_deviation_ms,
            "codec_summary": self.codec_summary,
            "issues": [i.to_dict() for i in sorted(self.issues, key=issue_sort_key)],
        }


def group_key(path: str, root: str) -> str:
    """Directory of a file label, used to group versions of one asset.

    Labels produced by the CLI are already relative to the scan root; tests
    and library callers may pass absolute paths, which are made relative.
    """

    rel = (
        os.path.relpath(os.path.abspath(path), os.path.abspath(root))
        if os.path.isabs(path) else os.path.normpath(path)
    )
    return os.path.dirname(rel).replace(os.sep, "/")


def _track_signature(track: Track) -> tuple[str, str, str]:
    params = ",".join(f"{k}={track.params[k]}" for k in sorted(track.params))
    return (track.kind, track.codec or UNKNOWN, params)


def _summary_by_kind(tracks: list[Track]) -> dict[str, list[str]]:
    summary: dict[str, list[str]] = {}
    for track in tracks:
        _, codec, _ = _track_signature(track)
        summary.setdefault(track.kind, []).append(codec)
    for values in summary.values():
        values.sort()
    return {kind: summary[kind] for kind in sorted(summary)}


def _kinds(tracks: list[Track]) -> list[str]:
    return sorted(t.kind for t in tracks)


def _unknown_fields(container: Container) -> list[str]:
    missing = []
    if container.timescale is None:
        missing.append("timescale")
    if container.duration_ms is None:
        missing.append("duration_ms")
    for track in container.tracks:
        label = f"track {track.track_id}" if track.track_id is not None else "track"
        if track.codec is None:
            missing.append(f"{label}.codec")
        if track.timescale is None:
            missing.append(f"{label}.timescale")
        if track.duration_ms is None:
            missing.append(f"{label}.duration_ms")
        if track.kind == UNKNOWN:
            missing.append(f"{label}.kind")
    return missing


def _file_issue(container: Container, code: str, rule: str, message: str,
                severity: str = "error", **extra: Any) -> Issue:
    return Issue(severity=severity, code=code, rule=rule, message=message,
                 path=container.path, **extra)


def _parse_issues(container: Container) -> list[Issue]:
    if container.error is None:
        return []
    suffix = f" at offset {container.error_offset}" if container.error_offset is not None else ""
    return [_file_issue(container, "container-error", "R001",
                        f"{container.error}{suffix}")]


def _problem_issues(container: Container) -> list[Issue]:
    issues = []
    for track in container.tracks:
        for problem in track.problems:
            token = problem.split(":", 1)[0]
            rule, code, severity = PROBLEM_RULES.get(
                token, ("R009", "unknown-metadata", "warning")
            )
            issues.append(Issue(
                severity=severity, code=code, rule=rule,
                message=f"track {track.track_id}: {problem}",
                path=container.path, track_id=track.track_id,
            ))
    return issues


def _compare_pair(ref: Container, other: Container, tolerance: int) -> list[Issue]:
    issues: list[Issue] = []
    if ref.error is not None or other.error is not None:
        return issues
    ref_kinds, other_kinds = _kinds(ref.tracks), _kinds(other.tracks)
    if len(ref.tracks) != len(other.tracks):
        issues.append(_file_issue(
            other, "track-count-drift", "R002",
            f"track count {len(other.tracks)} differs from reference "
            f"{os.path.basename(ref.path)} ({len(ref.tracks)})",
            expected=len(ref.tracks), actual=len(other.tracks),
        ))
    if ref_kinds != other_kinds:
        issues.append(_file_issue(
            other, "track-kind-drift", "R003",
            f"track kinds {other_kinds} differ from reference {ref_kinds}",
            expected=ref_kinds, actual=other_kinds,
        ))
    if ref.timescale is not None and other.timescale is not None:
        if ref.timescale != other.timescale:
            issues.append(_file_issue(
                other, "timescale-drift", "R004",
                f"time base {other.timescale} differs from reference {ref.timescale}",
                expected=ref.timescale, actual=other.timescale,
            ))
    if len(ref.tracks) == len(other.tracks):
        for old, new in zip(ref.tracks, other.tracks):
            if old.timescale is None or new.timescale is None:
                continue
            if old.timescale != new.timescale:
                issues.append(_file_issue(
                    other, "timescale-drift", "R004",
                    f"track time base {new.timescale} differs from reference "
                    f"{old.timescale}",
                    expected=old.timescale, actual=new.timescale,
                    track_id=new.track_id,
                ))
        ref_sigs = [_track_signature(t) for t in ref.tracks]
        other_sigs = [_track_signature(t) for t in other.tracks]
        if ref_sigs != other_sigs:
            issues.append(_file_issue(
                other, "codec-param-drift", "R005",
                "codec parameters differ from reference: "
                f"{other_sigs} != {ref_sigs}",
                expected=ref_sigs, actual=other_sigs,
            ))
    if ref.duration_ms is not None and other.duration_ms is not None:
        deviation = other.duration_ms - ref.duration_ms
        if abs(deviation) > tolerance:
            issues.append(_file_issue(
                other, "duration-deviation", "R006",
                f"duration {other.duration_ms} ms differs from reference "
                f"{ref.duration_ms} ms by {deviation:+d} ms "
                f"(tolerance {tolerance} ms)",
                expected=ref.duration_ms, actual=other.duration_ms,
                deviation_ms=deviation,
            ))
    return issues


def evaluate(containers: list[Container], root: str, duration_tolerance_ms: int = 0,
             strict: bool = False) -> BatchReport:
    """Apply all rules to a deterministic, path-sorted set of containers."""

    if duration_tolerance_ms < 0:
        raise ValueError("duration tolerance must be non-negative")
    ordered = sorted(containers, key=lambda c: c.path)
    report = BatchReport()
    groups: dict[str, list[Container]] = {}
    for container in ordered:
        groups.setdefault(group_key(container.path, root), []).append(container)

    for group in sorted(groups):
        members = groups[group]
        reference = members[0]
        for index, container in enumerate(members):
            file_issues = _parse_issues(container) + _problem_issues(container)
            if index > 0:
                file_issues.extend(
                    _compare_pair(reference, container, duration_tolerance_ms)
                )
            if container.error is None:
                for field_name in _unknown_fields(container):
                    file_issues.append(_file_issue(
                        container, "unknown-metadata", "R009",
                        f"{field_name} is unknown",
                        severity="error" if strict else "warning",
                        actual=UNKNOWN,
                    ))
            report.files.append(FileResult(container=container, issues=file_issues))
            report.issues.extend(file_issues)

    for group in sorted(groups):
        durations = [c.duration_ms for c in groups[group]
                     if c.duration_ms is not None]
        if durations:
            report.duration_deviation_ms = max(
                report.duration_deviation_ms, max(durations) - min(durations)
            )
    merged: dict[str, list[str]] = {}
    for container in ordered:
        for kind, codecs in _summary_by_kind(container.tracks).items():
            merged.setdefault(kind, []).extend(codecs)
    report.codec_summary = {
        kind: sorted(set(codecs)) for kind, codecs in sorted(merged.items())
    }
    report.issues.sort(key=issue_sort_key)
    return report
