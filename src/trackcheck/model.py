"""Domain models for tracks, media containers, mismatches and packages."""
from __future__ import annotations

from dataclasses import dataclass, field, asdict
from typing import Any

# Supported track kinds (byte values in the container).
KIND_VIDEO = 1
KIND_AUDIO = 2
KIND_SUBTITLE = 3
KIND_NAMES = {KIND_VIDEO: "video", KIND_AUDIO: "audio", KIND_SUBTITLE: "subtitle"}

# Known codec identifiers (4-byte ASCII tags).
CODEC_H264 = "H264"
CODEC_VP9 = "VP9 "
CODEC_AAC = "AAC "
CODEC_PCM = "PCM "
CODEC_SRT = "SRT "
KNOWN_CODECS = frozenset({CODEC_H264, CODEC_VP9, CODEC_AAC, CODEC_PCM, CODEC_SRT})

# Container hard limits guarding against hostile header declarations.
MAX_FILE_BYTES = 256 * 1024 * 1024
MAX_TRACKS = 64
SUPPORTED_VERSION = 1


@dataclass
class TrackError:
    """A diagnostic attached to a file or one of its tracks."""

    code: str
    message: str
    offset: int | None = None
    track_id: int | None = None

    def to_dict(self) -> dict[str, Any]:
        out: dict[str, Any] = {"code": self.code, "message": self.message}
        if self.offset is not None:
            out["offset"] = self.offset
        if self.track_id is not None:
            out["track_id"] = self.track_id
        return out


@dataclass
class Track:
    """Metadata extracted for a single track entry."""

    track_id: int
    kind: int
    kind_name: str
    codec_id: str
    timebase: int
    start_ts: int
    sample_count: int
    width: int
    height: int
    sample_rate: int
    channels: int
    language: str
    payload_offset: int
    payload_length: int
    flags: int
    unrecognized: bool = False
    raw_fields: dict[str, Any] = field(default_factory=dict)

    @property
    def duration_ms(self) -> float:
        """Track duration in milliseconds, derived from timebase and samples."""
        if self.timebase <= 0:
            return 0.0
        return self.sample_count * 1000.0 / self.timebase

    def to_dict(self) -> dict[str, Any]:
        return asdict(self) | {"duration_ms": round(self.duration_ms, 3)}


@dataclass
class Media:
    """Metadata extracted from one container file."""

    path: str
    version: int
    header_length: int
    track_count: int
    declared_length: int
    actual_length: int
    tracks: list[Track]
    errors: list[TrackError] = field(default_factory=list)
    content_hash: str = ""
    unrecognized_fields: dict[str, Any] = field(default_factory=dict)

    @property
    def valid(self) -> bool:
        return not self.errors

    @property
    def duration_ms(self) -> float:
        """Container duration: the longest track duration."""
        return max((t.duration_ms for t in self.tracks), default=0.0)

    def track_by_id(self, track_id: int) -> Track | None:
        for track in self.tracks:
            if track.track_id == track_id:
                return track
        return None

    def to_dict(self) -> dict[str, Any]:
        return {
            "path": self.path,
            "version": self.version,
            "header_length": self.header_length,
            "track_count": self.track_count,
            "declared_length": self.declared_length,
            "actual_length": self.actual_length,
            "content_hash": self.content_hash,
            "duration_ms": round(self.duration_ms, 3),
            "unrecognized_fields": self.unrecognized_fields,
            "tracks": [t.to_dict() for t in self.tracks],
            "errors": [e.to_dict() for e in self.errors],
        }


@dataclass
class Mismatch:
    """One difference located to a file and (where applicable) a track."""

    kind: str
    track_id: int | None
    message: str
    field: str | None = None
    baseline: Any = None
    input_value: Any = None
    delta_ms: float | None = None
    baseline_path: str = ""
    input_path: str = ""

    def to_dict(self) -> dict[str, Any]:
        out: dict[str, Any] = {
            "kind": self.kind,
            "track_id": self.track_id,
            "message": self.message,
        }
        if self.field is not None:
            out["field"] = self.field
        if self.baseline is not None:
            out["baseline"] = self.baseline
        if self.input_value is not None:
            out["input_value"] = self.input_value
        if self.delta_ms is not None:
            out["delta_ms"] = round(self.delta_ms, 3)
        if self.baseline_path:
            out["baseline_path"] = self.baseline_path
        if self.input_path:
            out["input_path"] = self.input_path
        return out


@dataclass
class ComparisonResult:
    """Outcome of comparing one baseline container against one input."""

    baseline_path: str
    input_path: str
    tolerance_ms: float
    baseline_tracks: int
    input_tracks: int
    baseline_duration_ms: float
    input_duration_ms: float
    duration_delta_ms: float
    mismatches: list[Mismatch] = field(default_factory=list)
    errors: list[TrackError] = field(default_factory=list)

    @property
    def ok(self) -> bool:
        return not self.mismatches and not self.errors

    def to_dict(self) -> dict[str, Any]:
        return {
            "baseline_path": self.baseline_path,
            "input_path": self.input_path,
            "tolerance_ms": self.tolerance_ms,
            "track_count": {
                "baseline": self.baseline_tracks,
                "input": self.input_tracks,
            },
            "duration_ms": {
                "baseline": round(self.baseline_duration_ms, 3),
                "input": round(self.input_duration_ms, 3),
            },
            "duration_delta_ms": round(self.duration_delta_ms, 3),
            "mismatch_count": len(self.mismatches),
            "mismatches": [m.to_dict() for m in self.mismatches],
            "errors": [e.to_dict() for e in self.errors],
        }


@dataclass
class PackageManifest:
    """Result of a successful package input check."""

    output_path: str
    source_path: str
    track_count: int
    tracks: list[Track]
    content_hash: str

    def to_dict(self) -> dict[str, Any]:
        return {
            "output_path": self.output_path,
            "source_path": self.source_path,
            "content_hash": self.content_hash,
            "track_count": self.track_count,
            "tracks": [t.to_dict() for t in self.tracks],
        }
