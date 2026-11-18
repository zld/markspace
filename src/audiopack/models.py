"""Data models: audio metadata, process configuration, and result statistics."""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path

MIN_CHANNELS = 1
MAX_CHANNELS = 8
SUPPORTED_EXTENSIONS = {".wav", ".flac"}
OUTPUT_SUBTYPE = "PCM_24"
OUTPUT_FORMAT = "WAV"


class AudioPackError(Exception):
    """Base error for per-file and batch-level failures."""


class DiscoveryError(AudioPackError):
    """Input discovery or batch-level validation failed."""


class DecodeError(AudioPackError):
    """A single input file could not be decoded or failed validation."""


class EncodeError(AudioPackError):
    """Writing or re-reading an output file failed."""


@dataclass(frozen=True)
class AudioMeta:
    """Parsed metadata of an audio file."""

    path: Path
    sample_rate: int
    channels: int
    frames: int
    format: str
    subtype: str


@dataclass(frozen=True)
class ProcessConfig:
    """User-supplied batch processing configuration."""

    input_dir: Path
    output_dir: Path
    sample_rate: int
    target_lufs: float
    jobs: int = 1
    peak_ceiling_dbfs: float = -1.0


@dataclass
class FileStats:
    """Quality metrics collected before/after processing one file."""

    sample_rate_in: int
    sample_rate_out: int
    channels: int
    frames_in: int
    frames_out: int
    rms_before_dbfs: float
    rms_after_dbfs: float
    lufs_before: float
    lufs_after: float
    gain_db: float
    peak_before_dbfs: float
    peak_after_dbfs: float
    clipped_samples_before: int
    clipped_samples_after: int


@dataclass
class FileResult:
    """Outcome of processing a single input file."""

    relpath: str
    status: str  # "ok" | "failed"
    error: str | None = None
    stats: FileStats | None = None


@dataclass
class BatchSummary:
    """Aggregate outcome of a batch run."""

    files_ok: int = 0
    files_failed: int = 0
    results: list[FileResult] = field(default_factory=list)
