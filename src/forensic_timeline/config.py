"""Configuration validation: timezone, drift limit, thresholds and input caps."""

from __future__ import annotations

import argparse
from dataclasses import dataclass
from datetime import tzinfo
from pathlib import Path
from zoneinfo import ZoneInfoNotFoundError

from .clock import resolve_timezone

MAX_DRIFT_LIMIT_SECONDS = 86_400          # one day
MAX_GAP_THRESHOLD_SECONDS = 7 * 86_400    # one week
MAX_BLOCKS_CAP = 10_000_000
MAX_IMAGE_BYTES_CAP = 1 << 34             # 16 GiB hard ceiling
MAX_RECORDS_CAP = 50_000_000


class ConfigError(Exception):
    """Raised for invalid command-line configuration (exit code 2)."""


@dataclass(frozen=True)
class Config:
    timezone_name: str
    timezone: tzinfo
    drift_limit_seconds: int
    gap_threshold_seconds: int
    db_path: Path | None
    json_path: Path | None
    max_blocks: int
    max_image_bytes: int
    max_records: int
    min_anchors: int = 2


def build_config(args: argparse.Namespace) -> Config:
    if not args.timezone:
        raise ConfigError("--timezone is required (IANA name, e.g. Asia/Shanghai)")
    try:
        tz = resolve_timezone(args.timezone)
    except (ZoneInfoNotFoundError, ValueError) as exc:
        raise ConfigError(f"unknown IANA timezone: {args.timezone!r}") from exc

    if not 0 < args.drift_limit_seconds <= MAX_DRIFT_LIMIT_SECONDS:
        raise ConfigError(
            f"--drift-limit-seconds must be in 1..{MAX_DRIFT_LIMIT_SECONDS}, "
            f"got {args.drift_limit_seconds}"
        )
    if not 0 < args.gap_threshold_seconds <= MAX_GAP_THRESHOLD_SECONDS:
        raise ConfigError(
            f"--gap-threshold-seconds must be in 1..{MAX_GAP_THRESHOLD_SECONDS}, "
            f"got {args.gap_threshold_seconds}"
        )
    if not 0 < args.max_blocks <= MAX_BLOCKS_CAP:
        raise ConfigError(f"--max-blocks must be in 1..{MAX_BLOCKS_CAP}")
    if not 0 < args.max_image_bytes <= MAX_IMAGE_BYTES_CAP:
        raise ConfigError(f"--max-image-bytes must be in 1..{MAX_IMAGE_BYTES_CAP}")
    if not 0 < args.max_records <= MAX_RECORDS_CAP:
        raise ConfigError(f"--max-records must be in 1..{MAX_RECORDS_CAP}")

    return Config(
        timezone_name=args.timezone,
        timezone=tz,
        drift_limit_seconds=args.drift_limit_seconds,
        gap_threshold_seconds=args.gap_threshold_seconds,
        db_path=Path(args.db) if args.db else None,
        json_path=Path(args.json) if args.json else None,
        max_blocks=args.max_blocks,
        max_image_bytes=args.max_image_bytes,
        max_records=args.max_records,
    )
