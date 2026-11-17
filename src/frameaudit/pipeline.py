"""Single-file processing pipeline.

Data flow (shared by the stdout summary and the per-frame JSONL):

    decode.py -> order.py -> [faults.py] -> repair.py -> stats.py -> report.py
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Any

from .decode import StreamInfo, iter_decoded_frames, open_video
from .faults import apply_faults, load_manifest
from .order import DEFAULT_REORDER_LIMIT, reorder_frames
from .repair import RepairResult, repair_timestamps, resolve_frame_rate
from .stats import FrameStats, compute_stats


@dataclass(slots=True)
class PipelineResult:
    summary: dict[str, Any]
    repair: RepairResult
    stats: FrameStats
    info: StreamInfo
    frame_rate: Any


def process_file(
    input_path: str | Path,
    stream_index: int = 0,
    explicit_fps: float | None = None,
    reorder_limit: int = DEFAULT_REORDER_LIMIT,
    faults_manifest: str | Path | None = None,
) -> PipelineResult:
    """Run the full audit pipeline over one media file."""
    input_path = Path(input_path)
    container, stream, info = open_video(input_path, stream_index)
    frame_rate = resolve_frame_rate(info.average_rate, explicit_fps)

    decoded = list(iter_decoded_frames(container, stream))
    ordered = list(reorder_frames(decoded, limit=reorder_limit))

    # Auto-discover a sidecar manifest next to the media when none is given.
    if faults_manifest is None:
        candidate = input_path.with_suffix(".faults.json")
        if candidate.is_file():
            faults_manifest = candidate

    if faults_manifest is not None:
        manifest = load_manifest(faults_manifest)
        ordered = apply_faults(ordered, manifest, frame_rate)
        manifest_source = str(faults_manifest)
    else:
        manifest = None
        manifest_source = None

    repair = repair_timestamps(ordered, frame_rate)
    stats = compute_stats(repair.records)

    from .report import build_summary

    summary = build_summary(
        source=str(input_path),
        stream_index=stream_index,
        codec_name=info.codec_name,
        frame_rate=frame_rate,
        stats=stats,
        repair=repair,
        reorder_limit=reorder_limit,
        fault_manifest=manifest_source,
    )
    return PipelineResult(
        summary=summary,
        repair=repair,
        stats=stats,
        info=info,
        frame_rate=frame_rate,
    )
