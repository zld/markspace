"""Test-facing fixture factory.

The deterministic builders live in the library (``media_index.fixtures`` and
``media_index._fixture_builders``) so the CLI can generate fixtures without
importing the test tree. This module re-exports them plus the layout
constants for use in pytest cases.
"""

from __future__ import annotations

from media_index._fixture_builders import (  # noqa: F401
    CHUNK_PLAN,
    CTTS_OFFSETS,
    SAMPLE_DELTA,
    SAMPLE_SIZES,
    SEGMENT_PLAN,
    SYNC_SAMPLES,
    TIMESCALE,
    ensure_dir,
)
from media_index.fixtures import (
    build_empty,
    build_nested,
    build_valid,
    inject_bad_timescale,
    inject_cycle,
    inject_duplicate_chunk_offset,
    inject_misaligned,
    inject_oversized_reference,
    inject_truncated_field,
    inject_truncated_header,
)

__all__ = [
    "CHUNK_PLAN",
    "CTTS_OFFSETS",
    "SAMPLE_DELTA",
    "SAMPLE_SIZES",
    "SEGMENT_PLAN",
    "SYNC_SAMPLES",
    "TIMESCALE",
    "build_empty",
    "build_nested",
    "build_valid",
    "ensure_dir",
    "inject_bad_timescale",
    "inject_cycle",
    "inject_duplicate_chunk_offset",
    "inject_misaligned",
    "inject_oversized_reference",
    "inject_truncated_field",
    "inject_truncated_header",
]
