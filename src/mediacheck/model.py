"""Unified media, track and issue model shared by every subsystem.

The JSON form of these dataclasses is the contract between the probe, the
comparator, the SQLite store and the packer, so the field names are kept
stable and serialisable.
"""

from __future__ import annotations

from dataclasses import dataclass, field as dc_field
from typing import Any

CONTAINER_MP4 = "mp4"
CONTAINER_MATROSKA = "matroska"

KIND_VIDEO = "video"
KIND_AUDIO = "audio"
KIND_SUBTITLE = "subtitle"
KIND_OTHER = "other"
KINDS = (KIND_VIDEO, KIND_AUDIO, KIND_SUBTITLE, KIND_OTHER)

SEVERITY_INFO = "info"
SEVERITY_WARNING = "warning"
SEVERITY_ERROR = "error"
SEVERITY_FATAL = "fatal"


@dataclass
class Issue:
    """A diagnostic tied to a byte offset where that is meaningful."""

    code: str
    message: str
    offset: int | None = None
    severity: str = SEVERITY_ERROR
    track_id: int | None = None

    def to_dict(self) -> dict[str, Any]:
        return {
            "code": self.code,
            "message": self.message,
            "offset": self.offset,
            "severity": self.severity,
            "track_id": self.track_id,
        }

    @classmethod
    def from_dict(cls, raw: dict[str, Any]) -> "Issue":
        return cls(
            code=raw["code"],
            message=raw["message"],
            offset=raw.get("offset"),
            severity=raw.get("severity", SEVERITY_ERROR),
            track_id=raw.get("track_id"),
        )


@dataclass
class Track:
    """One audio/video/subtitle track in the unified model."""

    track_id: int
    kind: str
    codec: str
    time_base: str
    duration_ms: float | None = None
    language: str | None = None
    offset: int | None = None
    codec_detail: dict[str, Any] = dc_field(default_factory=dict)
    issues: list[Issue] = dc_field(default_factory=list)

    @property
    def duration_us(self) -> int | None:
        if self.duration_ms is None:
            return None
        return int(round(self.duration_ms * 1000))

    def to_dict(self) -> dict[str, Any]:
        return {
            "track_id": self.track_id,
            "kind": self.kind,
            "codec": self.codec,
            "time_base": self.time_base,
            "duration_ms": None if self.duration_ms is None
            else round(self.duration_ms, 3),
            "language": self.language,
            "offset": self.offset,
            "codec_detail": dict(self.codec_detail),
            "issues": [issue.to_dict() for issue in self.issues],
        }

    @classmethod
    def from_dict(cls, raw: dict[str, Any]) -> "Track":
        return cls(
            track_id=raw["track_id"],
            kind=raw["kind"],
            codec=raw["codec"],
            time_base=raw["time_base"],
            duration_ms=raw.get("duration_ms"),
            language=raw.get("language"),
            offset=raw.get("offset"),
            codec_detail=dict(raw.get("codec_detail") or {}),
            issues=[Issue.from_dict(i) for i in raw.get("issues") or []],
        )


@dataclass
class MediaFile:
    """Everything the probe learned about one input file."""

    path: str
    size: int
    sha256: str | None
    container: str | None = None
    duration_ms: float | None = None
    tracks: list[Track] = dc_field(default_factory=list)
    issues: list[Issue] = dc_field(default_factory=list)
    fatal: bool = False

    @property
    def track_count(self) -> int:
        return len(self.tracks)

    def tracks_of_kind(self, kind: str) -> list[Track]:
        return [t for t in self.tracks if t.kind == kind]

    @property
    def probe_ok(self) -> bool:
        return not self.fatal

    def add_issue(self, issue: Issue) -> None:
        self.issues.append(issue)
        if issue.severity == SEVERITY_FATAL:
            self.fatal = True

    def to_dict(self) -> dict[str, Any]:
        return {
            "path": self.path,
            "size": self.size,
            "sha256": self.sha256,
            "container": self.container,
            "duration_ms": None if self.duration_ms is None
            else round(self.duration_ms, 3),
            "track_count": self.track_count,
            "tracks": [track.to_dict() for track in self.tracks],
            "issues": [issue.to_dict() for issue in self.issues],
            "fatal": self.fatal,
        }

    @classmethod
    def from_dict(cls, raw: dict[str, Any]) -> "MediaFile":
        return cls(
            path=raw["path"],
            size=raw["size"],
            sha256=raw.get("sha256"),
            container=raw.get("container"),
            duration_ms=raw.get("duration_ms"),
            tracks=[Track.from_dict(t) for t in raw.get("tracks") or []],
            issues=[Issue.from_dict(i) for i in raw.get("issues") or []],
            fatal=bool(raw.get("fatal")),
        )


@dataclass
class Mismatch:
    """One difference between a baseline and candidate track."""

    kind: str
    message: str
    baseline_track_id: int | None = None
    candidate_track_id: int | None = None
    field: str | None = None
    baseline_value: Any = None
    candidate_value: Any = None
    offset: int | None = None
    candidates: list[int] = dc_field(default_factory=list)

    def to_dict(self) -> dict[str, Any]:
        return {
            "kind": self.kind,
            "message": self.message,
            "baseline_track_id": self.baseline_track_id,
            "candidate_track_id": self.candidate_track_id,
            "field": self.field,
            "baseline_value": self.baseline_value,
            "candidate_value": self.candidate_value,
            "offset": self.offset,
            "candidates": list(self.candidates),
        }


STATUS_MATCH = "match"
STATUS_ISSUES = "issues"
STATUS_PROBE_ERROR = "probe_error"


@dataclass
class ValidationResult:
    baseline: MediaFile
    candidate: MediaFile
    tolerance_ms: float
    mismatches: list[Mismatch] = dc_field(default_factory=list)
    ignored_fields: list[str] = dc_field(default_factory=list)

    @property
    def status(self) -> str:
        if self.baseline.fatal or self.candidate.fatal:
            return STATUS_PROBE_ERROR
        return STATUS_ISSUES if self.mismatches else STATUS_MATCH

    @property
    def baseline_tracks(self) -> int:
        return self.baseline.track_count

    @property
    def candidate_tracks(self) -> int:
        return self.candidate.track_count

    @property
    def duration_delta_ms(self) -> float | None:
        a = self.baseline.duration_ms
        b = self.candidate.duration_ms
        if a is None or b is None:
            return None
        return round(b - a, 3)

    def to_dict(self) -> dict[str, Any]:
        return {
            "status": self.status,
            "baseline_tracks": self.baseline_tracks,
            "candidate_tracks": self.candidate_tracks,
            "duration_delta_ms": self.duration_delta_ms,
            "tolerance_ms": self.tolerance_ms,
            "ignored_fields": list(self.ignored_fields),
            "mismatch_count": len(self.mismatches),
            "mismatches": [m.to_dict() for m in self.mismatches],
            "baseline": self.baseline.to_dict(),
            "candidate": self.candidate.to_dict(),
        }
