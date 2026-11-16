"""Track matching and consistency comparison across a material group.

Files form a group when they share a stem (the delivery ``--reference``
name).  Tracks are matched by type and codec independent of their order in
the file; stable codec parameters and durations are then compared.  Every
inconsistency gets its own typed record instead of a single pass/fail bit.
"""

from __future__ import annotations

from dataclasses import dataclass, field, asdict
from pathlib import Path

from .container_probe import ProbeResult
from .tracks import Track

DUP_ID = "duplicate_track_id"
MISSING = "missing_track"
EXTRA = "extra_track"
PARAM_DIFF = "param_diff"
DURATION_DIFF = "duration_delta"
DURATION_MISSING = "missing_duration"


@dataclass(slots=True)
class Mismatch:
    group: str
    kind: str
    files: list[str]
    detail: str
    track_type: str | None = None
    codec: str | None = None
    parameter: str | None = None
    expected: object = None
    actual: object = None
    delta_ms: int | None = None

    def to_dict(self) -> dict:
        return asdict(self)


@dataclass(slots=True)
class GroupReport:
    group: str
    files: list[str]
    track_count: int
    max_duration_delta_ms: int | None = None
    mismatches: list[Mismatch] = field(default_factory=list)

    def to_dict(self) -> dict:
        return {
            "group": self.group,
            "files": self.files,
            "track_count": self.track_count,
            "max_duration_delta_ms": self.max_duration_delta_ms,
            "mismatches": [m.to_dict() for m in self.mismatches],
        }


@dataclass(slots=True)
class ScanComparison:
    groups: list[GroupReport] = field(default_factory=list)
    mismatches: list[Mismatch] = field(default_factory=list)
    max_duration_delta_ms: int | None = None

    def to_dict(self) -> dict:
        return {
            "groups": [g.to_dict() for g in self.groups],
            "max_duration_delta_ms": self.max_duration_delta_ms,
            "mismatches": [m.to_dict() for m in self.mismatches],
        }


def group_key(path: str) -> str:
    """Group identity: file stem, e.g. scene-a.mp4 and scene-a.mkv."""
    return Path(path).stem


def _identity(track: Track) -> tuple:
    return (track.track_type, track.codec)


def _match_pair(ref: Track, other: Track) -> int:
    """Lower score means a better match; identical stable params score 0."""
    if _identity(ref) != _identity(other):
        return 100
    score = 1
    for key, value in ref.stable_params().items():
        if other.stable_params().get(key) != value:
            score += 1
    return score


def match_tracks(reference: list[Track],
                 other: list[Track]) -> tuple[list[tuple[Track, Track]],
                                              list[Track], list[Track]]:
    """Order-independent one-to-one matching on (type, codec).

    Returns (pairs, tracks missing in ``other``, tracks extra in ``other``).
    """
    edges = sorted(
        ((_match_pair(r, o), i, j)
         for i, r in enumerate(reference)
         for j, o in enumerate(other)
         if _identity(r) == _identity(o)),
        key=lambda e: e[0],
    )
    used_ref: set[int] = set()
    used_other: set[int] = set()
    pairs: list[tuple[Track, Track]] = []
    for _, i, j in edges:
        if i in used_ref or j in used_other:
            continue
        used_ref.add(i)
        used_other.add(j)
        pairs.append((reference[i], other[j]))
    missing = [t for i, t in enumerate(reference) if i not in used_ref]
    extra = [t for i, t in enumerate(other) if i not in used_other]
    return pairs, missing, extra


def _duplicate_ids(group: str, path: str,
                   tracks: list[Track]) -> list[Mismatch]:
    seen: dict[object, int] = {}
    out: list[Mismatch] = []
    for track in tracks:
        if track.track_id is None:
            continue
        seen[track.track_id] = seen.get(track.track_id, 0) + 1
    for track_id, count in sorted(seen.items()):
        if count > 1:
            out.append(Mismatch(
                group=group, kind=DUP_ID, files=[path],
                detail=(f"track id {track_id} used {count} times in file"),
                parameter="track_id", expected=1, actual=count))
    return out


def _compare_pair(group: str, ref: Track, other: Track,
                  tolerance_ms: int) -> tuple[list[Mismatch], int | None]:
    mismatches: list[Mismatch] = []
    max_delta: int | None = None
    for key, value in ref.stable_params().items():
        actual = other.stable_params().get(key)
        if actual != value:
            mismatches.append(Mismatch(
                group=group, kind=PARAM_DIFF,
                files=[ref.file_path, other.file_path],
                detail=(f"{ref.track_type} track parameter {key} differs"),
                track_type=ref.track_type, codec=ref.codec,
                parameter=key, expected=value, actual=actual))
    if ref.duration_ms is None or other.duration_ms is None:
        if ref.duration_ms != other.duration_ms:
            mismatches.append(Mismatch(
                group=group, kind=DURATION_MISSING,
                files=[ref.file_path, other.file_path],
                detail="duration missing on one side of the pair",
                track_type=ref.track_type, codec=ref.codec))
    else:
        delta = abs(ref.duration_ms - other.duration_ms)
        max_delta = delta
        if delta > tolerance_ms:  # delta == tolerance is still consistent
            mismatches.append(Mismatch(
                group=group, kind=DURATION_DIFF,
                files=[ref.file_path, other.file_path],
                detail=(f"duration deviation {delta} ms exceeds tolerance "
                        f"{tolerance_ms} ms"),
                track_type=ref.track_type, codec=ref.codec,
                parameter="duration_ms", expected=ref.duration_ms,
                actual=other.duration_ms, delta_ms=delta))
    return mismatches, max_delta


def compare_results(results: list[ProbeResult],
                    tolerance_ms: int) -> ScanComparison:
    """Run the full group comparison over successful probe results."""
    ok = [r for r in results if r.ok]
    buckets: dict[str, list[ProbeResult]] = {}
    for result in sorted(ok, key=lambda r: r.path):
        buckets.setdefault(group_key(result.path), []).append(result)

    comparison = ScanComparison()
    overall_max: int | None = None
    for name, members in sorted(buckets.items()):
        report = GroupReport(
            group=name, files=[r.path for r in members],
            track_count=sum(len(r.tracks) for r in members))
        for member in members:
            report.mismatches.extend(
                _duplicate_ids(name, member.path, member.tracks))
        if len(members) > 1:
            reference = members[0]
            for member in members[1:]:
                pairs, missing, extra = match_tracks(
                    reference.tracks, member.tracks)
                for track in missing:
                    report.mismatches.append(Mismatch(
                        group=name, kind=MISSING,
                        files=[reference.path, member.path],
                        detail=(f"{track.track_type} track "
                                f"codec={track.codec} has no counterpart"),
                        track_type=track.track_type, codec=track.codec))
                for track in extra:
                    report.mismatches.append(Mismatch(
                        group=name, kind=EXTRA, files=[member.path],
                        detail=(f"unexpected {track.track_type} track "
                                f"codec={track.codec}"),
                        track_type=track.track_type, codec=track.codec))
                for ref_track, other_track in pairs:
                    pair_mm, delta = _compare_pair(
                        name, ref_track, other_track, tolerance_ms)
                    report.mismatches.extend(pair_mm)
                    if delta is not None:
                        report.max_duration_delta_ms = (
                            delta if report.max_duration_delta_ms is None
                            else max(report.max_duration_delta_ms, delta))
        if report.max_duration_delta_ms is not None:
            overall_max = (report.max_duration_delta_ms
                           if overall_max is None
                           else max(overall_max,
                                    report.max_duration_delta_ms))
        comparison.groups.append(report)
        comparison.mismatches.extend(report.mismatches)
    comparison.max_duration_delta_ms = overall_max
    return comparison
