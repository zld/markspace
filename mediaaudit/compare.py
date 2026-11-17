"""Manifest model, extraction pipeline and cross-file comparison.

Track records from the MP4 and Matroska parsers are unified here, mapped
to the semantic roles declared in the manifest and compared against
expectations and tolerances. Extraction may run concurrently; results are
always returned in manifest order.
"""
from __future__ import annotations

import concurrent.futures
import hashlib
import json
import os
from dataclasses import dataclass, field

from . import mkv, mp4, sniff

DEFAULT_TOLERANCES = {"duration_ms": 10, "frame_rate": 0.01}


class ManifestError(Exception):
    """Fatal manifest problem (exit code 2)."""


@dataclass
class Issue:
    kind: str
    path: str
    detail: str
    role: str = ""

    def to_dict(self) -> dict:
        return {"kind": self.kind, "path": self.path, "role": self.role,
                "detail": self.detail}


@dataclass
class FileRecord:
    path: str
    container: str = ""
    sha256: str = ""
    size: int = 0
    tracks: list = field(default_factory=list)
    error: str = ""
    rejected: bool = False


@dataclass
class Report:
    show_id: str
    files: int
    tracks: int
    track_count: dict
    duration_delta_ms: dict
    max_duration_delta_ms: int
    codec_summary: dict
    mismatches: list
    per_file: list

    @property
    def status(self) -> str:
        return "fail" if self.mismatches else "pass"

    def to_dict(self) -> dict:
        return {
            "show_id": self.show_id,
            "status": self.status,
            "files": self.files,
            "tracks": self.tracks,
            "track_count": self.track_count,
            "duration_delta_ms": self.duration_delta_ms,
            "max_duration_delta_ms": self.max_duration_delta_ms,
            "codec_summary": self.codec_summary,
            "mismatches": [i.to_dict() for i in self.mismatches],
            "per_file": self.per_file,
        }


def load_manifest(path: str) -> dict:
    try:
        with open(path, "r", encoding="utf-8") as fh:
            data = json.load(fh)
    except (OSError, json.JSONDecodeError) as exc:
        raise ManifestError(f"cannot read manifest {path}: {exc}") from exc
    if not isinstance(data, dict):
        raise ManifestError("manifest must be a JSON object")
    if not isinstance(data.get("show_id"), str) or not data["show_id"]:
        raise ManifestError("manifest requires a non-empty 'show_id'")
    files = data.get("files")
    if (not isinstance(files, list) or not files
            or not all(isinstance(f, str) and f for f in files)):
        raise ManifestError("manifest requires a non-empty 'files' list of paths")
    tracks = data.get("tracks")
    if not isinstance(tracks, list) or not tracks:
        raise ManifestError("manifest requires a non-empty 'tracks' list")
    for spec in tracks:
        if (not isinstance(spec, dict) or not spec.get("role")
                or spec.get("type") not in ("video", "audio", "other")):
            raise ManifestError("each track needs 'role' and a valid 'type'")
    toler = data.get("tolerances", {})
    if not isinstance(toler, dict):
        raise ManifestError("'tolerances' must be an object")
    merged = dict(DEFAULT_TOLERANCES)
    merged.update(toler)
    data["tolerances"] = merged
    data.setdefault("expect", {})
    return data


def _extract_one(base_dir: str, rel: str, max_bytes: int) -> FileRecord:
    full = os.path.join(base_dir, rel)
    try:
        container = sniff.sniff(full, base_dir, max_bytes)
        with open(full, "rb") as fh:
            data = fh.read()
        digest = hashlib.sha256(data).hexdigest()
        if container == sniff.CONTAINER_MP4:
            tracks = mp4.parse_mp4_bytes(data)
        else:
            tracks = mkv.parse_mkv_bytes(data)
        return FileRecord(path=rel, container=container, sha256=digest,
                          size=len(data), tracks=tracks)
    except sniff.SniffError as exc:
        return FileRecord(path=rel, error=str(exc), rejected=True)
    except (mp4.MP4Error, mkv.MKVError, OSError) as exc:
        return FileRecord(path=rel, error=str(exc))


def extract_all(manifest_dir: str, manifest: dict, workers: int = 1,
                max_bytes: int = sniff.DEFAULT_MAX_BYTES) -> list:
    files = manifest["files"]
    if workers > 1 and len(files) > 1:
        with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as pool:
            return list(pool.map(
                lambda rel: _extract_one(manifest_dir, rel, max_bytes), files))
    return [_extract_one(manifest_dir, rel, max_bytes) for rel in files]


def _check_expectations(path, role, track, expect, toler, issues):
    def add(kind, detail):
        issues.append(Issue(kind, path, detail, role))

    if "codec" in expect and track["codec"] != expect["codec"]:
        add("codec_mismatch",
            f"expected codec {expect['codec']}, got {track['codec']}")
    for key, kind in (("width", "resolution_mismatch"),
                      ("height", "resolution_mismatch"),
                      ("sample_rate", "sample_rate_mismatch"),
                      ("channels", "channels_mismatch")):
        if key in expect and track.get(key) != expect[key]:
            add(kind, f"expected {key} {expect[key]}, got {track.get(key)}")
    if "frame_rate" in expect:
        rate = track.get("frame_rate")
        if rate is None or abs(rate - expect["frame_rate"]) > toler["frame_rate"]:
            add("frame_rate_mismatch",
                f"expected frame_rate {expect['frame_rate']}, got {rate}")


def _assign_roles(rec: FileRecord, roles: list, issues: list) -> dict:
    seen, dups = set(), set()
    for t in rec.tracks:
        if t["track_id"] in seen:
            dups.add(t["track_id"])
        seen.add(t["track_id"])
    for tid in sorted(dups):
        issues.append(Issue("duplicate_track_id", rec.path,
                            f"track id {tid} appears more than once"))
    by_type = {}
    for t in rec.tracks:
        by_type.setdefault(t["type"], []).append(t)
    assigned, used = {}, set()
    for spec in roles:
        role, rtype = spec["role"], spec["type"]
        candidate = next((t for t in by_type.get(rtype, [])
                          if id(t) not in used), None)
        if candidate is not None:
            used.add(id(candidate))
            assigned[role] = candidate
        elif spec.get("required", False):
            issues.append(Issue("missing_track", rec.path,
                                f"required {rtype} track for role '{role}' "
                                f"is absent", role))
    return assigned


def compare_records(records: list, manifest: dict) -> Report:
    toler = manifest["tolerances"]
    roles = manifest["tracks"]
    expect = manifest["expect"]
    issues, track_count = [], {}
    per_file = []
    role_durations = {r["role"]: [] for r in roles}
    role_codecs = {r["role"]: set() for r in roles}
    total_tracks = 0

    for rec in records:
        if rec.error:
            kind = "rejected_path" if rec.rejected else "extract_failed"
            issues.append(Issue(kind, rec.path, rec.error))
            track_count[rec.path] = 0
            per_file.append({"path": rec.path, "error": rec.error, "tracks": []})
            continue
        assigned = _assign_roles(rec, roles, issues)
        file_tracks = []
        for role, t in assigned.items():
            if t["codec"].startswith("unknown:"):
                issues.append(Issue("unknown_codec", rec.path,
                                    f"unrecognized codec '{t['codec_raw']}'", role))
            _check_expectations(rec.path, role, t, expect.get(role, {}),
                                toler, issues)
            dur_ms = t["duration_ns"] // 1_000_000
            role_durations[role].append((rec.path, dur_ms))
            role_codecs[role].add(t["codec"])
            file_tracks.append({
                "role": role, "type": t["type"], "track_id": t["track_id"],
                "codec": t["codec"], "duration_ms": dur_ms,
                "width": t["width"], "height": t["height"],
                "frame_rate": t["frame_rate"], "sample_rate": t["sample_rate"],
                "channels": t["channels"],
            })
        total_tracks += len(rec.tracks)
        track_count[rec.path] = len(rec.tracks)
        per_file.append({
            "path": rec.path, "container": rec.container, "sha256": rec.sha256,
            "size": rec.size, "track_count": len(rec.tracks),
            "tracks": file_tracks,
        })

    duration_delta, max_delta = {}, 0
    for role, pairs in role_durations.items():
        if not pairs:
            duration_delta[role] = 0
            continue
        ref_path, ref = pairs[0]
        values = [d for _, d in pairs]
        delta = max(values) - min(values)
        duration_delta[role] = delta
        max_delta = max(max_delta, delta)
        for path, dur in pairs[1:]:
            if abs(dur - ref) > toler["duration_ms"]:
                issues.append(Issue(
                    "duration_delta", path,
                    f"duration {dur}ms deviates {abs(dur - ref)}ms from "
                    f"reference {ref_path} ({ref}ms); tolerance "
                    f"{toler['duration_ms']}ms", role))

    return Report(
        show_id=manifest["show_id"],
        files=len(records),
        tracks=total_tracks,
        track_count=track_count,
        duration_delta_ms=duration_delta,
        max_duration_delta_ms=max_delta,
        codec_summary={r: sorted(c) for r, c in role_codecs.items()},
        mismatches=issues,
        per_file=per_file,
    )
