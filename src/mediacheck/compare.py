"""Policy file loading and validation for compare.

A policy is JSON. Syntax errors report line/column; semantic errors report
a dotted field path such as ``matching.pairs[0].reference_track_id`` so a
bad policy can always be located without guessing.
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from . import POLICY_VERSION


class PolicyError(Exception):
    def __init__(self, message: str, *, field_path: str | None = None,
                 line: int | None = None, column: int | None = None):
        super().__init__(message)
        self.message = message
        self.field_path = field_path
        self.line = line
        self.column = column

    def to_dict(self) -> dict:
        return {
            "code": "policy_error",
            "message": self.message,
            "field_path": self.field_path,
            "line": self.line,
            "column": self.column,
        }


@dataclass
class TrackPair:
    reference_track_id: int
    target_track_id: int


@dataclass
class DurationPolicy:
    tolerance_seconds: float = 0.02
    tolerance_ratio: float = 0.0
    start_severity: str = "warning"     # error | warning | ignore


@dataclass
class MatchingPolicy:
    pairs: list[TrackPair] = field(default_factory=list)
    require_kinds: list[str] = field(default_factory=list)
    ignore_kinds: list[str] = field(default_factory=list)
    match_by_index: bool = False


@dataclass
class CodecPolicy:
    whitelist: dict[str, list[dict[str, Any]]] = field(default_factory=dict)
    warn_fields: set[str] = field(default_factory=set)
    ignore_fields: set[str] = field(default_factory=set)
    missing_severity: str = "info"      # error | warning | info
    enabled: bool = True


@dataclass
class Policy:
    name: str = "default"
    version: int = POLICY_VERSION
    duration: DurationPolicy = field(default_factory=DurationPolicy)
    matching: MatchingPolicy = field(default_factory=MatchingPolicy)
    codec: CodecPolicy = field(default_factory=CodecPolicy)

    def to_dict(self) -> dict:
        return {
            "name": self.name,
            "version": self.version,
            "duration": {
                "tolerance_seconds": self.duration.tolerance_seconds,
                "tolerance_ratio": self.duration.tolerance_ratio,
                "start_severity": self.duration.start_severity,
            },
            "matching": {
                "pairs": [
                    {"reference_track_id": p.reference_track_id,
                     "target_track_id": p.target_track_id}
                    for p in self.matching.pairs
                ],
                "require_kinds": list(self.matching.require_kinds),
                "ignore_kinds": list(self.matching.ignore_kinds),
                "match_by_index": self.matching.match_by_index,
            },
            "codec": {
                "enabled": self.codec.enabled,
                "whitelist": self.codec.whitelist,
                "warn_fields": sorted(self.codec.warn_fields),
                "ignore_fields": sorted(self.codec.ignore_fields),
                "missing_severity": self.codec.missing_severity,
            },
        }


_SEVERITIES = {"error", "warning", "info", "ignore"}
_KINDS = {"video", "audio", "subtitle", "timecode", "hint", "other"}


def _fail(message: str, path: str, **kwargs) -> PolicyError:
    return PolicyError(message, field_path=path, **kwargs)


def _expect_type(value: Any, expected: type | tuple, path: str,
                 label: str) -> None:
    if not isinstance(value, expected):
        names = (expected.__name__ if isinstance(expected, type)
                 else "/".join(t.__name__ for t in expected))
        raise _fail(f"expected {names}, got {type(value).__name__}", path)


def _number(value: Any, path: str, *, minimum: float = 0.0) -> float:
    _expect_type(value, (int, float), path, "number")
    if isinstance(value, bool) or value < minimum:
        raise _fail(f"must be a number >= {minimum}, got {value!r}", path)
    return float(value)


def _severity(value: Any, path: str, *, allow_ignore: bool = True) -> str:
    _expect_type(value, str, path, "string")
    allowed = _SEVERITIES if allow_ignore else _SEVERITIES - {"ignore"}
    if value not in allowed:
        raise _fail(f"must be one of {sorted(allowed)}, got {value!r}", path)
    return value


def _string_list(value: Any, path: str) -> list[str]:
    _expect_type(value, list, path, "list")
    for i, item in enumerate(value):
        _expect_type(item, str, f"{path}[{i}]", "string")
    return list(value)


def _known_keys(obj: dict, allowed: set[str], path: str) -> None:
    unknown = sorted(set(obj) - allowed)
    if unknown:
        raise _fail(f"unknown field(s) {unknown}; allowed are {sorted(allowed)}",
                    f"{path}.{unknown[0]}".lstrip("."))


def parse_policy(data: Any, *, name: str = "inline") -> Policy:
    """Validate an already-parsed policy document."""
    _expect_type(data, dict, "$", "object")
    _known_keys(data, {"name", "version", "duration", "matching", "codec"}, "$")
    policy = Policy(name=str(data.get("name", name)))
    if "version" in data:
        _expect_type(data["version"], int, "version", "integer")
        if data["version"] != POLICY_VERSION:
            raise _fail(f"unsupported policy version {data['version']}; "
                        f"this build understands {POLICY_VERSION}", "version")
    if "duration" in data:
        policy.duration = _parse_duration(data["duration"])
    if "matching" in data:
        policy.matching = _parse_matching(data["matching"])
    if "codec" in data:
        policy.codec = _parse_codec(data["codec"])
    return policy


def _parse_duration(raw: Any) -> DurationPolicy:
    _expect_type(raw, dict, "duration", "object")
    _known_keys(raw, {"tolerance_seconds", "tolerance_ratio", "start_severity"},
                "duration")
    out = DurationPolicy()
    if "tolerance_seconds" in raw:
        out.tolerance_seconds = _number(raw["tolerance_seconds"],
                                        "duration.tolerance_seconds")
    if "tolerance_ratio" in raw:
        out.tolerance_ratio = _number(raw["tolerance_ratio"],
                                      "duration.tolerance_ratio")
    if "start_severity" in raw:
        out.start_severity = _severity(raw["start_severity"],
                                       "duration.start_severity")
    return out


def _parse_matching(raw: Any) -> MatchingPolicy:
    _expect_type(raw, dict, "matching", "object")
    _known_keys(raw, {"pairs", "require_kinds", "ignore_kinds", "match_by_index"},
                "matching")
    out = MatchingPolicy()
    if "pairs" in raw:
        _expect_type(raw["pairs"], list, "matching.pairs", "list")
        for i, item in enumerate(raw["pairs"]):
            path = f"matching.pairs[{i}]"
            _expect_type(item, dict, path, "object")
            _known_keys(item, {"reference_track_id", "target_track_id"}, path)
            for key in ("reference_track_id", "target_track_id"):
                if key not in item:
                    raise _fail(f"missing required field {key!r}", f"{path}.{key}")
                _expect_type(item[key], int, f"{path}.{key}", "integer")
            out.pairs.append(TrackPair(item["reference_track_id"],
                                       item["target_track_id"]))
    if "require_kinds" in raw:
        out.require_kinds = _string_list(raw["require_kinds"],
                                         "matching.require_kinds")
        for i, kind in enumerate(out.require_kinds):
            if kind not in _KINDS:
                raise _fail(f"unknown track kind {kind!r}",
                            f"matching.require_kinds[{i}]")
    if "ignore_kinds" in raw:
        out.ignore_kinds = _string_list(raw["ignore_kinds"],
                                        "matching.ignore_kinds")
        for i, kind in enumerate(out.ignore_kinds):
            if kind not in _KINDS:
                raise _fail(f"unknown track kind {kind!r}",
                            f"matching.ignore_kinds[{i}]")
    if "match_by_index" in raw:
        _expect_type(raw["match_by_index"], bool, "matching.match_by_index",
                     "boolean")
        out.match_by_index = raw["match_by_index"]
    return out


_CODEC_FIELDS = {"codec", "width", "height", "sample_rate", "channels",
                 "bit_depth", "language"}


def _parse_codec(raw: Any) -> CodecPolicy:
    _expect_type(raw, dict, "codec", "object")
    _known_keys(raw, {"enabled", "whitelist", "warn_fields", "ignore_fields",
                      "missing_severity"}, "codec")
    out = CodecPolicy()
    if "enabled" in raw:
        _expect_type(raw["enabled"], bool, "codec.enabled", "boolean")
        out.enabled = raw["enabled"]
    if "warn_fields" in raw:
        out.warn_fields = set(_string_list(raw["warn_fields"],
                                           "codec.warn_fields"))
    if "ignore_fields" in raw:
        out.ignore_fields = set(_string_list(raw["ignore_fields"],
                                             "codec.ignore_fields"))
    if "missing_severity" in raw:
        out.missing_severity = _severity(raw["missing_severity"],
                                         "codec.missing_severity")
    if "whitelist" in raw:
        wl = raw["whitelist"]
        _expect_type(wl, dict, "codec.whitelist", "object")
        for kind, entries in wl.items():
            path = f"codec.whitelist.{kind}"
            if kind not in _KINDS:
                raise _fail(f"unknown track kind {kind!r}", path)
            _expect_type(entries, list, path, "list")
            parsed_entries = []
            for i, entry in enumerate(entries):
                epath = f"{path}[{i}]"
                _expect_type(entry, dict, epath, "object")
                _known_keys(entry, _CODEC_FIELDS, epath)
                if "codec" not in entry:
                    raise _fail("whitelist entry requires a 'codec' field",
                                f"{epath}.codec")
                _expect_type(entry["codec"], str, f"{epath}.codec", "string")
                for key, value in entry.items():
                    if key == "codec":
                        continue
                    _expect_type(value, (int, str), f"{epath}.{key}",
                                 "integer or string")
                parsed_entries.append(dict(entry))
            out.whitelist[kind] = parsed_entries
    return out


def load_policy(path: str | Path | None,
                *, tolerance_seconds: float | None = None) -> Policy:
    """Load a policy file and apply CLI overrides."""
    if path is None:
        policy = Policy()
    else:
        target = Path(path)
        try:
            raw = target.read_text(encoding="utf-8")
        except OSError as exc:
            raise PolicyError(f"cannot read policy {target}: {exc}") from None
        try:
            data = json.loads(raw)
        except json.JSONDecodeError as exc:
            raise PolicyError(
                f"invalid JSON in {target}: {exc.msg}",
                line=exc.lineno, column=exc.colno) from None
        policy = parse_policy(data, name=target.stem)
    if tolerance_seconds is not None:
        if tolerance_seconds < 0:
            raise PolicyError("--duration-tolerance must be >= 0",
                              field_path="duration.tolerance_seconds")
        policy.duration.tolerance_seconds = float(tolerance_seconds)
    return policy


# ---------------------------------------------------------------------------
# Comparison
# ---------------------------------------------------------------------------

SEVERITY_RANK = {"info": 0, "warning": 1, "error": 2}
HARD = "error"


@dataclass
class Issue:
    code: str
    severity: str
    message: str
    scope: str
    path: str | None = None
    reference: Any = None
    target: Any = None
    detail: dict[str, Any] = field(default_factory=dict)

    def to_dict(self) -> dict:
        return {
            "code": self.code,
            "severity": self.severity,
            "message": self.message,
            "scope": self.scope,
            "path": self.path,
            "reference": self.reference,
            "target": self.target,
            "detail": self.detail,
        }


def _index_media(manifest: dict, scope: str) -> dict[str, dict]:
    """Map relative paths to media summaries; duplicates are reported later."""
    if not isinstance(manifest, dict):
        raise PolicyError(f"{scope} manifest must be a JSON object")
    media = manifest.get("media")
    if not isinstance(media, list):
        raise PolicyError(f"{scope} manifest is missing a 'media' list",
                          field_path="media")
    out: dict[str, dict] = {}
    duplicates: list[str] = []
    for i, entry in enumerate(media):
        if not isinstance(entry, dict):
            raise PolicyError(f"{scope} media entry {i} is not an object",
                              field_path=f"media[{i}]")
        key = entry.get("relative_path") or entry.get("name") or entry.get("path")
        if not key:
            raise PolicyError(f"{scope} media entry {i} has no name or path",
                              field_path=f"media[{i}].name")
        key = str(key)
        if key in out:
            duplicates.append(key)
            continue
        out[key] = entry
    if duplicates:
        # A duplicate path means the manifest itself is inconsistent; refuse
        # rather than silently dropping whichever entry came second.
        raise PolicyError(
            f"{scope} manifest lists {len(set(duplicates))} path(s) more than "
            f"once: {sorted(set(duplicates))[:5]}",
            field_path="media")
    return out


def _track_sort(track: dict) -> tuple:
    track_id = track.get("track_id")
    return (0 if track_id is not None else 1,
            track_id if track_id is not None else 0,
            track.get("index", 0))


class TrackMatcher:
    """Pair reference and target tracks without ever guessing on ambiguity."""

    def __init__(self, policy: Policy):
        self.policy = policy
        mp = policy.matching
        self.explicit = {p.reference_track_id: p.target_track_id
                         for p in mp.pairs}
        self.explicit_targets = {p.target_track_id: p.reference_track_id
                                 for p in mp.pairs}
        self.ignore = set(mp.ignore_kinds)
        self.require = set(mp.require_kinds)

    def compare(self, ref_tracks: list[dict], tgt_tracks: list[dict]
                ) -> tuple[list[tuple[dict, dict]], list[Issue]]:
        issues: list[Issue] = []
        pairs: list[tuple[dict, dict]] = []
        used_ref: set[int] = set()
        used_tgt: set[int] = set()

        # 1. Explicit track_id mapping wins. It is applied before the ignore
        #    filter because an explicit pair is more specific than a kind rule.
        for ref in ref_tracks:
            rid = ref.get("track_id")
            if rid is None or rid not in self.explicit:
                continue
            wanted = self.explicit[rid]
            matches = [t for t in tgt_tracks if t.get("track_id") == wanted]
            if len(matches) != 1:
                issues.append(Issue(
                    "explicit_pair_missing", HARD,
                    f"policy pairs reference track_id {rid} with target "
                    f"track_id {wanted}, but the target has {len(matches)} such tracks",
                    scope="track", reference=rid, target=wanted))
                continue
            pairs.append((ref, matches[0]))
            used_ref.add(id(ref))
            used_tgt.add(id(matches[0]))

        # 2. Remaining tracks pair by kind, but only when unambiguous.
        usable_ref = [t for t in ref_tracks if self._usable(t)]
        usable_tgt = [t for t in tgt_tracks if self._usable(t)]
        remaining_ref = [t for t in usable_ref if id(t) not in used_ref]
        remaining_tgt = [t for t in usable_tgt if id(t) not in used_tgt]
        kinds = sorted({t.get("kind", "other") for t in remaining_ref}
                       | {t.get("kind", "other") for t in remaining_tgt})
        for kind in kinds:
            refs = sorted((t for t in remaining_ref if t.get("kind", "other") == kind),
                          key=_track_sort)
            tgts = sorted((t for t in remaining_tgt if t.get("kind", "other") == kind),
                          key=_track_sort)
            if self.policy.matching.match_by_index and len(refs) == len(tgts):
                if self._indexes_unique(refs) and self._indexes_unique(tgts):
                    for ref, tgt in zip(refs, tgts):
                        pairs.append((ref, tgt))
                        used_ref.add(id(ref))
                        used_tgt.add(id(tgt))
                    continue
            if len(refs) == 1 and len(tgts) == 1:
                pairs.append((refs[0], tgts[0]))
                used_ref.add(id(refs[0]))
                used_tgt.add(id(tgts[0]))
                continue
            if len(refs) > 1 and len(tgts) > 1:
                issues.append(Issue(
                    "ambiguous_tracks", HARD,
                    f"{len(refs)} reference and {len(tgts)} target {kind} tracks "
                    "cannot be uniquely matched; add explicit track_id pairs",
                    scope="track", reference=[t.get("track_id") for t in refs],
                    target=[t.get("track_id") for t in tgts],
                    detail={"kind": kind}))
                for t in refs:
                    used_ref.add(id(t))
                for t in tgts:
                    used_tgt.add(id(t))
                continue
            if len(refs) > 1:
                issues.append(Issue(
                    "extra_reference_tracks", HARD,
                    f"{len(refs)} reference {kind} tracks but only {len(tgts)} "
                    "target track(s); cannot decide which one is authoritative",
                    scope="track", reference=[t.get("track_id") for t in refs],
                    detail={"kind": kind}))
                for t in refs:
                    used_ref.add(id(t))
                for t in tgts:
                    used_tgt.add(id(t))
                continue
            if len(tgts) > 1:
                issues.append(Issue(
                    "extra_target_tracks", HARD,
                    f"{len(tgts)} target {kind} tracks but no reference "
                    f"{kind} track; cannot pick one",
                    scope="track", target=[t.get("track_id") for t in tgts],
                    detail={"kind": kind}))
                for t in tgts:
                    used_tgt.add(id(t))
                continue

        for ref in remaining_ref:
            if id(ref) in used_ref:
                continue
            issues.append(Issue(
                "missing_target_track", HARD,
                f"reference {ref.get('kind')} track {ref.get('track_id')} "
                "has no target counterpart",
                scope="track", reference=ref.get("track_id"),
                detail={"kind": ref.get("kind")}))
        for tgt in remaining_tgt:
            if id(tgt) in used_tgt:
                continue
            issues.append(Issue(
                "unexpected_target_track", HARD,
                f"target {tgt.get('kind')} track {tgt.get('track_id')} "
                "has no reference counterpart",
                scope="track", target=tgt.get("track_id"),
                detail={"kind": tgt.get("kind")}))
        for kind in sorted(self.require):
            if not any(t.get("kind") == kind for t in ref_tracks):
                issues.append(Issue(
                    "required_kind_missing", HARD,
                    f"policy requires a {kind} track in the reference, none found",
                    scope="track", detail={"kind": kind}))
        pairs.sort(key=lambda pair: (_track_sort(pair[0]), _track_sort(pair[1])))
        return pairs, issues

    def _usable(self, track: dict) -> bool:
        kind = track.get("kind", "other")
        if kind in self.ignore:
            return False
        return True

    @staticmethod
    def _indexes_unique(tracks: list[dict]) -> bool:
        values = [t.get("index") for t in tracks]
        return len(values) == len(set(values))


class Comparator:
    def __init__(self, policy: Policy):
        self.policy = policy

    def compare(self, reference: dict, target: dict) -> dict[str, Any]:
        ref_media = _index_media(reference, "reference")
        tgt_media = _index_media(target, "target")
        issues: list[Issue] = []
        pairs: list[dict] = []

        ref_label = reference.get("label") or reference.get("root")
        tgt_label = target.get("label") or target.get("root")

        for key in sorted(ref_media):
            ref = ref_media[key]
            tgt = tgt_media.get(key)
            if tgt is None:
                issues.append(Issue(
                    "missing_target_media", HARD,
                    f"{key} is in the reference package but absent from the target",
                    scope="file", path=key))
                continue
            file_issues, file_pairs = self._compare_media(key, ref, tgt)
            issues.extend(file_issues)
            pairs.extend(file_pairs)
        for key in sorted(set(tgt_media) - set(ref_media)):
            issues.append(Issue(
                "unexpected_target_media", HARD,
                f"{key} is in the target package but absent from the reference",
                scope="file", path=key))

        issues.sort(key=lambda i: (-SEVERITY_RANK[i.severity], i.scope,
                                   i.path or "", i.code))
        counts = {"error": 0, "warning": 0, "info": 0}
        for issue in issues:
            counts[issue.severity] += 1
        hard = counts["error"]
        return {
            "policy_version": POLICY_VERSION,
            "policy": self.policy.to_dict(),
            "reference": {"label": ref_label, "files": len(ref_media)},
            "target": {"label": tgt_label, "files": len(tgt_media)},
            "files_compared": len(set(ref_media) & set(tgt_media)),
            "duration_delta_seconds": max(
                (abs(p["duration_delta_seconds"]) for p in pairs
                 if p.get("duration_delta_seconds") is not None), default=0.0),
            "duration_delta_ratio": max(
                (abs(p["duration_delta_ratio"]) for p in pairs
                 if p.get("duration_delta_ratio") is not None), default=0.0),
            "inconsistency_count": len(issues),
            "hard_inconsistency_count": hard,
            "counts": counts,
            "issues": [i.to_dict() for i in issues],
            "pairs": pairs,
            "codec_summary": _collect_codecs(ref_media, tgt_media),
            "ok": hard == 0,
        }


def _collect_codecs(ref_media: dict[str, dict],
                    tgt_media: dict[str, dict]) -> list[str]:
    codecs: set[str] = set()
    for side in (ref_media, tgt_media):
        for media in side.values():
            for track in media.get("tracks", []):
                if track.get("codec"):
                    codecs.add(str(track["codec"]))
    return sorted(codecs)


def _effective_start(track: dict) -> float | None:
    return track.get("start_seconds")


def _effective_end(track: dict) -> float | None:
    start = _effective_start(track)
    duration = track.get("duration_seconds")
    if start is None or duration is None:
        return None
    return start + duration


def _compare_media(self, key: str, ref: dict, tgt: dict
                   ) -> tuple[list[Issue], list[dict]]:
    issues: list[Issue] = []
    pairs_out: list[dict] = []
    if ref.get("container") != tgt.get("container"):
        issues.append(Issue(
            "container_mismatch", "warning",
            f"{key}: container differs ({ref.get('container')} vs {tgt.get('container')})",
            scope="file", path=key, reference=ref.get("container"),
            target=tgt.get("container")))

    ref_tracks = ref.get("tracks") or []
    tgt_tracks = tgt.get("tracks") or []
    matcher = TrackMatcher(self.policy)
    pairs, match_issues = matcher.compare(ref_tracks, tgt_tracks)
    for issue in match_issues:
        issue.path = key
        issues.append(issue)

    for ref_track, tgt_track in pairs:
        track_issues, result = self._compare_track_pair(key, ref_track, tgt_track)
        issues.extend(track_issues)
        pairs_out.append(result)

    # File-level duration uses the union of matched tracks on one time base.
    ref_duration = ref.get("duration_seconds")
    tgt_duration = tgt.get("duration_seconds")
    file_check = self._duration_check(ref_duration, tgt_duration,
                                      scope="file", path=key,
                                      label="media duration")
    if file_check:
        issues.append(file_check)
    return issues, pairs_out


def _duration_check(self, ref_s: float | None, tgt_s: float | None, *,
                    scope: str, path: str | None, label: str,
                    track_pair: dict | None = None) -> Issue | None:
    dp = self.policy.duration
    if ref_s is None or tgt_s is None:
        return Issue(
            "duration_unavailable", "warning",
            f"{path or label}: duration unavailable "
            f"(reference={ref_s!r}, target={tgt_s!r})",
            scope=scope, path=path, reference=ref_s, target=tgt_s)
    delta = tgt_s - ref_s
    ratio = (delta / ref_s) if ref_s else None
    allowed = dp.tolerance_seconds
    if ratio is not None:
        allowed = max(allowed, dp.tolerance_ratio * abs(ref_s))
    over = abs(delta) > allowed + 1e-9
    if track_pair is not None:
        track_pair["duration_delta_seconds"] = round(delta, 6)
        track_pair["duration_delta_ratio"] = (round(ratio, 6)
                                              if ratio is not None else None)
        track_pair["duration_within_tolerance"] = not over
    if not over:
        return None
    return Issue(
        "duration_delta_exceeded", HARD,
        f"{path or label}: {label} differs by {delta:+.3f}s "
        f"({(ratio or 0.0) * 100:+.2f}%), tolerance {allowed:.3f}s",
        scope=scope, path=path, reference=ref_s, target=tgt_s,
        detail={"delta_seconds": round(delta, 6),
                "delta_ratio": round(ratio, 6) if ratio is not None else None,
                "tolerance_seconds": allowed})


def _compare_track_pair(self, key: str, ref: dict, tgt: dict
                        ) -> tuple[list[Issue], dict]:
    issues: list[Issue] = []
    pair_id = f"{key}#{ref.get('track_id')}->{tgt.get('track_id')}"
    result = {
        "path": key,
        "reference_track_id": ref.get("track_id"),
        "target_track_id": tgt.get("track_id"),
        "kind": ref.get("kind"),
        "duration_delta_seconds": None,
        "duration_delta_ratio": None,
        "duration_within_tolerance": None,
        "codec_matches": None,
        "start_delta_seconds": None,
    }
    if ref.get("kind") != tgt.get("kind"):
        issues.append(Issue(
            "track_kind_mismatch", HARD,
            f"{pair_id}: kind {ref.get('kind')} vs {tgt.get('kind')}",
            scope="track", path=key, reference=ref.get("kind"),
            target=tgt.get("kind")))

    ref_d = ref.get("duration_seconds")
    tgt_d = tgt.get("duration_seconds")
    check = self._duration_check(ref_d, tgt_d, scope="track", path=key,
                                 label=f"track {pair_id}", track_pair=result)
    if check:
        check.detail.setdefault("track_pair", {
            "reference_track_id": ref.get("track_id"),
            "target_track_id": tgt.get("track_id")})
        issues.append(check)

    ref_start = _effective_start(ref)
    tgt_start = _effective_start(tgt)
    if ref_start is not None and tgt_start is not None:
        delta = tgt_start - ref_start
        result["start_delta_seconds"] = round(delta, 6)
        tolerance = self.policy.duration.tolerance_seconds
        if abs(delta) > tolerance + 1e-9 and self.policy.duration.start_severity != "ignore":
            issues.append(Issue(
                "start_time_mismatch", self.policy.duration.start_severity,
                f"{pair_id}: start time differs by {delta:+.3f}s "
                f"(tolerance {tolerance:.3f}s)",
                scope="track", path=key, reference=ref_start, target=tgt_start,
                detail={"delta_seconds": round(delta, 6)}))

    codec_issues = self._compare_codec(key, ref, tgt, pair_id)
    issues.extend(codec_issues)
    result["codec_matches"] = not any(i.code.startswith("codec")
                                      and i.severity == HARD
                                      for i in codec_issues)
    return issues, result


_CODEC_COMPARE_FIELDS = ("codec", "width", "height", "sample_rate", "channels",
                         "bit_depth", "language")
# Fields that only make sense for a given track kind; comparing the others
# would report "missing" noise on every audio or video track.
_FIELDS_BY_KIND = {
    "video": {"codec", "width", "height", "language"},
    "audio": {"codec", "sample_rate", "channels", "bit_depth", "language"},
    "subtitle": {"codec", "language"},
    "timecode": {"codec"},
    "hint": {"codec"},
    "other": {"codec", "language"},
}


def _compare_codec(self, key: str, ref: dict, tgt: dict, pair_id: str
                   ) -> list[Issue]:
    cp = self.policy.codec
    if not cp.enabled:
        return []
    issues: list[Issue] = []
    kind = ref.get("kind") or tgt.get("kind") or "other"
    allowed_entries = cp.whitelist.get(kind)
    relevant = _FIELDS_BY_KIND.get(kind, {"codec"})
    for field in _CODEC_COMPARE_FIELDS:
        if field not in relevant:
            continue
        if field in cp.ignore_fields:
            continue
        rv, tv = ref.get(field), tgt.get(field)
        label = f"{pair_id}: {field}"
        if rv is None or tv is None:
            if rv is None and tv is None:
                # Neither side carries the field (for example a WAV track has
                # no language); there is nothing to reconcile.
                continue
            severity = cp.missing_severity
            if severity == "ignore":
                continue
            issues.append(Issue(
                "codec_missing_field", severity,
                f"{label} missing on {'reference' if rv is None else 'target'}"
                f" (reference={rv!r}, target={tv!r})",
                scope="track", path=key, reference=rv, target=tv,
                detail={"field": field}))
            continue
        if rv == tv:
            continue
        severity = "warning" if field in cp.warn_fields else HARD
        permitted = False
        if allowed_entries is not None:
            # A difference is tolerated only when some whitelist entry names
            # the exact target value and is consistent with the target codec.
            for entry in allowed_entries:
                if field == "codec":
                    if entry.get("codec") == tv:
                        permitted = True
                        break
                elif (entry.get("codec", tgt.get("codec")) == tgt.get("codec")
                      and entry.get(field) == tv):
                    permitted = True
                    break
        if permitted:
            issues.append(Issue(
                "codec_whitelisted", "info",
                f"{label} differs ({rv!r} -> {tv!r}) but {tv!r} is whitelisted",
                scope="track", path=key, reference=rv, target=tv,
                detail={"field": field}))
            continue
        issues.append(Issue(
            "codec_mismatch", severity,
            f"{label} differs: {rv!r} vs {tv!r}",
            scope="track", path=key, reference=rv, target=tv,
            detail={"field": field}))
    return issues


Comparator._compare_media = _compare_media
Comparator._duration_check = _duration_check
Comparator._compare_track_pair = _compare_track_pair
Comparator._compare_codec = _compare_codec
