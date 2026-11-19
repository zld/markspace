"""Wire decoding, reordering, timestamp repair and statistics together.

Both the terminal summary and the JSON report call :func:`run_audit`, so the
two output paths can never diverge: they consume the exact same
:class:`~frameaudit.model.AuditStats` produced by one shared decode -> reorder
-> repair pipeline.
"""

from __future__ import annotations

import os
from collections import Counter
from fractions import Fraction
from typing import Optional

from .decode import DEFAULT_MAX_FRAMES, VideoInput
from .errors import UsageError
from .model import AuditStats, FRAME_TYPES
from .reorder import DEFAULT_WINDOW_SIZE, reorder_frames
from .timestamps import repair_timestamps


def resolve_fps(
    cli_fps: Optional[Fraction],
    stream_average_rate: Optional[str],
) -> Optional[Fraction]:
    """Prefer an explicit CLI rate; fall back to the stream average rate."""
    if cli_fps is not None:
        return cli_fps
    if not stream_average_rate:
        return None
    try:
        return Fraction(stream_average_rate)
    except (ValueError, ZeroDivisionError):
        return None


def run_audit(
    input_path: str | os.PathLike[str],
    fps: Optional[Fraction] = None,
    jump_threshold: float = 2.0,
    window_size: int = DEFAULT_WINDOW_SIZE,
    max_frames: int = DEFAULT_MAX_FRAMES,
    frame_interval: Optional[Fraction] = None,
) -> AuditStats:
    """Run the full audit and return aggregated, output-ready statistics."""
    if jump_threshold <= 0:
        raise UsageError(
            f"jump threshold must be positive, got {jump_threshold}"
        )
    if window_size <= 0:
        raise UsageError(
            f"reorder window size must be positive, got {window_size}"
        )
    if max_frames <= 0:
        raise UsageError(f"max frames must be positive, got {max_frames}")
    if fps is not None and frame_interval is not None:
        raise UsageError("--fps and --frame-interval are mutually exclusive")

    with VideoInput(input_path) as video:
        metadata = video.metadata
        frames = list(video.iter_frames(max_frames=max_frames))

    ordered, overflow_count = reorder_frames(
        frames, window_size=window_size, max_frames=max_frames
    )

    effective_fps = resolve_fps(fps, metadata.get("average_rate"))
    normalized, repaired_count, max_jump_seconds = repair_timestamps(
        ordered,
        fps=effective_fps,
        jump_threshold=jump_threshold,
        explicit_interval=frame_interval,
    )

    counts = Counter(frame.frame_type for frame in frames)
    frame_type_counts = {label: int(counts.get(label, 0)) for label in FRAME_TYPES}

    return AuditStats(
        input_path=os.fspath(input_path),
        container_format=metadata.get("container_format"),
        video_codec=metadata.get("video_codec"),
        time_base=metadata.get("time_base"),
        average_rate=metadata.get("average_rate"),
        decoded_frames=len(frames),
        frame_type_counts=frame_type_counts,
        repaired_timestamps=repaired_count,
        max_jump_seconds=max_jump_seconds,
        reorder_overflows=overflow_count,
        frames=normalized,
    )
