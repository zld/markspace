"""Core data structures shared across the pipeline."""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime
from enum import Enum


class Confidence(str, Enum):
    HIGH = "high"
    LOW = "low"


class IntegrityStatus(str, Enum):
    OK = "ok"
    HASH_MISMATCH = "hash_mismatch"      # image block hash differs from manifest
    MISSING_BLOCK = "missing_block"      # manifest entry has no matching image block
    EXTRA_BLOCK = "extra_block"          # image block not listed in the manifest
    BAD_HASH_FORMAT = "bad_hash_format"  # manifest hash value is not 64 hex chars


@dataclass
class BlockInfo:
    index: int
    offset: int
    length: int
    sha256: str


@dataclass
class RawRecord:
    """One record line as read from the image, before time parsing."""

    event_id: str
    raw_timestamp: str
    event_type: str
    description: str
    source_block: int
    source_offset: int  # absolute byte offset of the record line in the image
    seq: int            # parse order, used as a stable sort tiebreaker


@dataclass
class Event:
    event_id: str
    raw_timestamp: str   # original timestamp text, never rewritten
    raw_timezone: str    # explicit offset or the IANA zone that was applied
    naive: bool          # True when the record carried no explicit offset
    utc_raw: datetime    # UTC before clock-drift correction
    utc_final: datetime  # UTC after correction (== utc_raw when not applied)
    corrected: bool
    confidence: Confidence
    event_type: str
    description: str
    source_block: int
    source_offset: int
    seq: int
    duplicate_of: str | None = None


@dataclass
class Anomaly:
    kind: str
    detail: str
    block_index: int | None = None
    offset: int | None = None


@dataclass
class IntegrityResult:
    block_index: int
    offset: int | None
    expected: str | None
    actual: str | None
    status: IntegrityStatus


@dataclass
class GapInterval:
    start_utc: datetime
    end_utc: datetime
    duration_seconds: int
    kind: str  # head | interior | tail


@dataclass
class ClockEstimate:
    drift_seconds: int
    anchors_used: int
    applied: bool
    confidence: Confidence
    reason: str
