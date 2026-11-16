"""Manifest-driven group comparison and stable mismatch reporting.

A manifest lists material groups; every group names its member files (relative
to the scan root) and the expected track inventory. Comparison is purely
functional over :class:`media_audit.probe.MediaInfo` records so it can run on
fresh probes or on rows reloaded from the SQLite store.
"""

from __future__ import annotations

import json
import os
from dataclasses import dataclass, field
from typing import Any

from .probe import MediaInfo, TrackInfo


class ManifestError(Exception):
    """Raised when a manifest file is missing or structurally invalid."""


@dataclass(frozen=True)
class Mismatch:
    kind: str
    group: str
    file: str
    detail: str
    track_id: int | None = None
    track_type: str | None = None

    def sort_key(self) -> tuple:
        return (
            self.kind,
            self.group,
            self.file,
            self.track_type or "",
            self.track_id if self.track_id is not None else -1,
            self.detail,
        )

    def to_dict(self) -> dict[str, Any]:
        return {
            "kind": self.kind,
            "group": self.group,
            "file": self.file,
            "track_id": self.track_id,
            "track_type": self.track_type,
            "detail": self.detail,
        }


@dataclass
class GroupResult:
    name: str
    files: list[str]
    duration_delta_ms: int | None = None
    mismatches: list[Mismatch] = field(default_factory=list)


@dataclass
class AuditResult:
    mismatches: list[Mismatch] = field(default_factory=list)
    groups: dict[str, GroupResult] = field(default_factory=dict)
    max_duration_delta_ms: int | None = None
    skipped: list[dict[str, str]] = field(default_factory=list)

    def to_dict(self) -> dict[str, Any]:
        return {
            "mismatches": [m.to_dict() for m in self.mismatches],
            "duration_delta_ms": self.max_duration_delta_ms,
            "groups": {
                name: {
                    "files": gr.files,
                    "duration_delta_ms": gr.duration_delta_ms,
                }
                for name, gr in sorted(self.groups.items())
            },
            "skipped": self.skipped,
        }


def load_manifest(path: str) -> dict[str, Any]:
    try:
        with open(path, "rb") as handle:
            raw = handle.read()
    except OSError as exc:
        raise ManifestError(f"cannot read manifest {path}: {exc}") from exc
    try:
        manifest = json.loads(raw.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ManifestError(f"invalid JSON in manifest {path}: {exc}") from exc
    if not isinstance(manifest, dict) or not isinstance(
        manifest.get("groups"), list
    ):
        raise ManifestError("manifest must be an object with a 'groups' array")
    for index, group in enumerate(manifest["groups"]):
        if not isinstance(group, dict) or not isinstance(group.get("name"), str):
            raise ManifestError(f"group #{index} lacks a string 'name'")
        if not isinstance(group.get("files"), list) or not group["files"]:
            raise ManifestError(
                f"group {group['name']!r} lacks a non-empty 'files' array"
            )
        expect = group.get("expect", [])
        if not isinstance(expect, list):
            raise ManifestError(f"group {group['name']!r}: 'expect' must be a list")
    return manifest


def _check_file_tracks(info: MediaInfo, group: str) -> list[Mismatch]:
    findings: list[Mismatch] = []
    seen_ids: dict[int, TrackInfo] = {}
    for track in info.tracks:
        if track.track_id is not None:
            if track.track_id in seen_ids:
                findings.append(
                    Mismatch(
                        kind="duplicate_track_id",
                        group=group,
                        file=info.name,
                        track_id=track.track_id,
                        track_type=track.type,
                        detail=(
                            f"track id {track.track_id} reused by "
                            f"{seen_ids[track.track_id].type} and {track.type}"
                        ),
                    )
                )
            else:
                seen_ids[track.track_id] = track
        if track.hidden:
            findings.append(
                Mismatch(
                    kind="hidden_track",
                    group=group,
                    file=info.name,
                    track_id=track.track_id,
                    track_type=track.type,
                    detail=f"{track.type} track {track.track_id} is hidden/disabled",
                )
            )
        if track.type in ("audio", "subtitle") and not track.language:
            findings.append(
                Mismatch(
                    kind="missing_language",
                    group=group,
                    file=info.name,
                    track_id=track.track_id,
                    track_type=track.type,
                    detail=f"{track.type} track {track.track_id} has no language tag",
                )
            )
    return findings


def _check_expectations(
    infos: list[MediaInfo], group: str, expect: list[dict[str, Any]]
) -> list[Mismatch]:
    findings: list[Mismatch] = []
    for spec in expect:
        wanted_type = spec.get("type")
        wanted_count = int(spec.get("count", 1))
        wanted_languages = spec.get("languages")
        wanted_family = spec.get("codec_family")
        for info in infos:
            tracks = [t for t in info.tracks if t.type == wanted_type]
            if len(tracks) != wanted_count:
                findings.append(
                    Mismatch(
                        kind="track_count",
                        group=group,
                        file=info.name,
                        track_type=wanted_type,
                        detail=(
                            f"expected {wanted_count} {wanted_type} track(s), "
                            f"found {len(tracks)}"
                        ),
                    )
                )
            if wanted_languages:
                have = {t.language for t in tracks}
                for language in wanted_languages:
                    if language not in have:
                        findings.append(
                            Mismatch(
                                kind="missing_expected_language",
                                group=group,
                                file=info.name,
                                track_type=wanted_type,
                                detail=(
                                    f"no {wanted_type} track tagged {language!r}"
                                ),
                            )
                        )
            if wanted_family:
                for track in tracks:
                    if track.codec_family != wanted_family:
                        findings.append(
                            Mismatch(
                                kind="codec_conflict",
                                group=group,
                                file=info.name,
                                track_id=track.track_id,
                                track_type=wanted_type,
                                detail=(
                                    f"expected codec family {wanted_family}, "
                                    f"found {track.codec_family} "
                                    f"({track.codec})"
                                ),
                            )
                        )
    return findings
def _check_cross_file(
    infos: list[MediaInfo], group: str, tolerance_ms: int
) -> tuple[list[Mismatch], int | None]:
    findings: list[Mismatch] = []
    # Codec summaries must agree per track type across every group member.
    type_per_file: dict[str, dict[str, list[str]]] = {}
    for info in infos:
        for track in info.tracks:
            type_per_file.setdefault(track.type, {}).setdefault(
                info.name, []
            ).append(track.codec_summary)
    for track_type, per_file in sorted(type_per_file.items()):
        # A type absent from a file is reported via count/expectation rules;
        # parameter comparison only runs where at least two files have it.
        present = {name: sums for name, sums in per_file.items() if sums}
        if len(present) < 2:
            continue
        signatures = {tuple(sorted(summaries)) for summaries in present.values()}
        if len(signatures) > 1:
            findings.append(
                Mismatch(
                    kind="parameter_conflict",
                    group=group,
                    file=",".join(sorted(present)),
                    track_type=track_type,
                    detail=(
                        f"{track_type} codec parameters differ across files: "
                        + "; ".join(
                            f"{name}={sorted(summaries)}"
                            for name, summaries in sorted(present.items())
                        )
                    ),
                )
            )

    # Duration: unknown values stay unknown and never count as zero.
    known = {
        info.name: info.duration_ms
        for info in infos
        if info.duration_ms is not None
    }
    for info in infos:
        if info.duration_ms is None and info.ok:
            findings.append(
                Mismatch(
                    kind="duration_unknown",
                    group=group,
                    file=info.name,
                    detail="container duration could not be determined",
                )
            )
    delta: int | None = None
    if len(known) >= 2:
        delta = max(known.values()) - min(known.values())
        if delta > tolerance_ms:
            findings.append(
                Mismatch(
                    kind="duration_conflict",
                    group=group,
                    file=",".join(sorted(known)),
                    detail=(
                        f"duration spread {delta}ms exceeds tolerance "
                        f"{tolerance_ms}ms "
                        f"(min={min(known.values())}, max={max(known.values())})"
                    ),
                )
            )
    return findings, delta


def compare_batch(
    infos_by_name: dict[str, MediaInfo],
    manifest: dict[str, Any],
    tolerance_ms: int,
) -> AuditResult:
    """Compare probed files against every manifest group."""
    result = AuditResult()
    for group_spec in manifest["groups"]:
        group = group_spec["name"]
        members: list[MediaInfo] = []
        group_result = GroupResult(name=group, files=list(group_spec["files"]))
        for member_name in group_spec["files"]:
            info = infos_by_name.get(member_name)
            if info is None:
                group_result.mismatches.append(
                    Mismatch(
                        kind="missing_file",
                        group=group,
                        file=member_name,
                        detail="manifest member absent from scan results",
                    )
                )
                continue
            members.append(info)
            group_result.mismatches.extend(_check_file_tracks(info, group))
        group_result.mismatches.extend(
            _check_expectations(members, group, group_spec.get("expect", []))
        )
        cross, delta = _check_cross_file(members, group, tolerance_ms)
        group_result.mismatches.extend(cross)
        group_result.duration_delta_ms = delta
        result.groups[group] = group_result
        result.mismatches.extend(group_result.mismatches)
        if delta is not None and (
            result.max_duration_delta_ms is None
            or delta > result.max_duration_delta_ms
        ):
            result.max_duration_delta_ms = delta
    result.mismatches.sort(key=lambda item: item.sort_key())
    return result


def resolve_manifest_path(input_root: str, manifest_path: str) -> str:
    """Reject manifests that escape the allowed input root."""
    root = os.path.abspath(input_root)
    candidate = os.path.abspath(manifest_path)
    if candidate != root and not candidate.startswith(root + os.sep):
        raise ManifestError(
            f"manifest {manifest_path} lies outside input root {input_root}"
        )
    return candidate
