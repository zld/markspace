"""Data model shared by extractors, comparator, catalog and packager."""
from __future__ import annotations

from dataclasses import asdict, dataclass, field


@dataclass
class CodecParams:
    codec: str                      # "avc1" | "aac" | "pcm"
    width: int | None = None
    height: int | None = None
    sample_rate: int | None = None
    channels: int | None = None
    profile: str | None = None

    def summary(self) -> str:
        if self.codec == "avc1":
            return f"H.264 {self.width}x{self.height}"
        if self.codec == "aac":
            return f"AAC {self.sample_rate}Hz/{self.channels}ch"
        if self.codec == "pcm":
            return f"PCM {self.sample_rate}Hz/{self.channels}ch"
        return self.codec


@dataclass
class TrackInfo:
    track_id: int
    kind: str                       # "video" | "audio"
    codec: CodecParams
    duration_ms: int
    language: str | None = None
    timescale: int = 0
    locator: str = ""               # e.g. "trak[1]" for error context

    def to_dict(self) -> dict:
        return asdict(self)


@dataclass
class MediaInfo:
    path: str                       # relative to the input root
    format: str                     # "mp4" | "wav"
    size_bytes: int
    sha256: str
    tracks: list[TrackInfo] = field(default_factory=list)

    def to_dict(self) -> dict:
        d = asdict(self)
        d["tracks"] = [t.to_dict() for t in self.tracks]
        return d


@dataclass
class Inconsistency:
    code: str                       # machine-readable, e.g. "duration-delta"
    path: str                       # asset relative path
    message: str
    track: str | None = None        # track locator, e.g. "trak[1]" / "audio:en"
    offset: int | None = None       # byte offset inside the file, if known
    detail: dict = field(default_factory=dict)

    def to_dict(self) -> dict:
        return asdict(self)


class MediaError(Exception):
    """Fatal per-file extraction failure; becomes one inconsistency."""

    def __init__(self, code: str, message: str, offset: int | None = None):
        super().__init__(message)
        self.code = code
        self.message = message
        self.offset = offset


class ManifestError(Exception):
    """Bad manifest, unsafe path or bad CLI parameter (exit code 3)."""


class StorageError(Exception):
    """Fatal catalog/SQLite failure (exit code 3)."""
