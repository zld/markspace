"""Data model for probed containers, tracks, and consistency issues."""

from __future__ import annotations

import json
from dataclasses import asdict, dataclass, field
from typing import Any

UNKNOWN = "unknown"
KNOWN_ROLES = ("video", "audio", "subtitle")
KNOWN_CODECS = ("h264", "aac", "srt")


@dataclass
class Track:
    """One track discovered inside a container."""

    track_id: int | None
    kind: str
    codec: str | None
    timescale: int | None
    start_ms: int | None
    duration_ms: int | None
    params: dict[str, Any] = field(default_factory=dict)
    problems: list[str] = field(default_factory=list)

    def to_dict(self) -> dict[str, Any]:
        # json round-trip coerces tuples in params to lists and proves it is serialisable.
        return json.loads(json.dumps(asdict(self), sort_keys=True))

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "Track":
        return cls(
            track_id=data.get("track_id"),
            kind=data.get("kind", UNKNOWN),
            codec=data.get("codec"),
            timescale=data.get("timescale"),
            start_ms=data.get("start_ms"),
            duration_ms=data.get("duration_ms"),
            params=dict(data.get("params") or {}),
            problems=list(data.get("problems") or []),
        )


@dataclass
class Container:
    """Metadata extracted from one media file."""

    path: str
    container: str = UNKNOWN
    tracks: list[Track] = field(default_factory=list)
    timescale: int | None = None
    start_ms: int | None = None
    duration_ms: int | None = None
    sha256: str = ""
    size: int = 0
    error: str | None = None
    error_offset: int | None = None

    def to_dict(self) -> dict[str, Any]:
        return json.loads(json.dumps(asdict(self), sort_keys=True))

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "Container":
        return cls(
            path=data.get("path", ""),
            container=data.get("container", UNKNOWN),
            tracks=[Track.from_dict(t) for t in data.get("tracks") or []],
            timescale=data.get("timescale"),
            start_ms=data.get("start_ms"),
            duration_ms=data.get("duration_ms"),
            sha256=data.get("sha256", ""),
            size=data.get("size", 0),
            error=data.get("error"),
            error_offset=data.get("error_offset"),
        )


@dataclass(frozen=True)
class Issue:
    """A single consistency problem with its triggering rule."""

    severity: str
    code: str
    rule: str
    message: str
    path: str | None = None
    track_id: int | None = None
    expected: Any = None
    actual: Any = None
    deviation_ms: int | None = None

    def to_dict(self) -> dict[str, Any]:
        return json.loads(json.dumps(asdict(self), sort_keys=True))

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "Issue":
        known = {f.name for f in cls.__dataclass_fields__.values()}  # type: ignore[attr-defined]
        return cls(**{k: v for k, v in data.items() if k in known})


@dataclass
class FileResult:
    """Container metadata joined with the issues raised for that file."""

    container: Container
    issues: list[Issue] = field(default_factory=list)

    def to_dict(self, issues: list[Issue] | None = None) -> dict[str, Any]:
        selected = self.issues if issues is None else issues
        data = self.container.to_dict()
        data["issues"] = [i.to_dict() for i in sorted(selected, key=issue_sort_key)]
        return data


def issue_sort_key(issue: Issue) -> tuple[str, int, str, str]:
    return (issue.path or "", issue.track_id if issue.track_id is not None else -1,
            issue.code, issue.message)
