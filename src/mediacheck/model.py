"""Unified track record shared by the sniffers, comparer and store.

Every field that could not be read from a container is left as ``None``.
The tool never substitutes guessed defaults: unknown means unknown.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any


@dataclass(slots=True)
class Track:
    """One logical track (audio/video/...) extracted from a container."""

    stable_index: int
    kind: str  # "audio" | "video" | "subtitle" | "other"
    language: str | None = None
    codec: str | None = None
    duration_ms: float | None = None
    sample_rate: int | None = None
    channels: int | None = None
    bit_depth: int | None = None
    block_align: int | None = None
    extra: dict[str, Any] = field(default_factory=dict)

    def to_dict(self) -> dict[str, Any]:
        return {
            "stable_index": self.stable_index,
            "kind": self.kind,
            "language": self.language,
            "codec": self.codec,
            "duration_ms": self.duration_ms,
            "sample_rate": self.sample_rate,
            "channels": self.channels,
            "bit_depth": self.bit_depth,
            "block_align": self.block_align,
            "extra": dict(self.extra),
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "Track":
        allowed = {
            "stable_index",
            "kind",
            "language",
            "codec",
            "duration_ms",
            "sample_rate",
            "channels",
            "bit_depth",
            "block_align",
        }
        kwargs = {k: data.get(k) for k in allowed}
        return cls(**kwargs, extra=dict(data.get("extra") or {}))


@dataclass(slots=True)
class SniffResult:
    """Everything the sniffer learned about one file."""

    container: str  # "wav" | "mp4" | "unknown"
    size: int
    tracks: list[Track] = field(default_factory=list)
    declared_duration_ms: float | None = None
    warnings: list[str] = field(default_factory=list)
    error: str | None = None
    truncated: bool = False
    metadata: dict[str, Any] = field(default_factory=dict)

    @property
    def ok(self) -> bool:
        return self.error is None

    def to_dict(self) -> dict[str, Any]:
        return {
            "container": self.container,
            "size": self.size,
            "declared_duration_ms": self.declared_duration_ms,
            "tracks": [t.to_dict() for t in self.tracks],
            "warnings": list(self.warnings),
            "error": self.error,
            "truncated": self.truncated,
            "metadata": dict(self.metadata),
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "SniffResult":
        return cls(
            container=data.get("container", "unknown"),
            size=int(data.get("size") or 0),
            tracks=[Track.from_dict(t) for t in data.get("tracks") or []],
            declared_duration_ms=data.get("declared_duration_ms"),
            warnings=list(data.get("warnings") or []),
            error=data.get("error"),
            truncated=bool(data.get("truncated")),
            metadata=dict(data.get("metadata") or {}),
        )


class MediaError(Exception):
    """Raised for structural container errors with a locatable offset."""

    def __init__(self, message: str, offset: int | None = None):
        self.offset = offset
        if offset is not None:
            message = f"{message} (offset {offset})"
        super().__init__(message)
