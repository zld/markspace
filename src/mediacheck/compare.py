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
